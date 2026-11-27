package site

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

type document struct {
	Schema               int       `json:"schema"`
	Kind                 string    `json:"kind"`
	Revision             string    `json:"revision"`
	UpdatedAt            time.Time `json:"updated_at"`
	Settings             Settings  `json:"settings"`
	discussionPresent    bool
	discussionPolicyPresent bool
	writingSafetyPresent bool
	integrationsPresent  bool
}

func loadDocument(path string) (Settings, time.Time, error) {
	file, _, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		// A Windows process interruption can leave the rollback copy between the
		// two rename operations. Treat it as the last committed document.
		file, _, err = packagefs.OpenRegularNoSymlink(path + ".bak")
	}
	if err != nil {
		return Settings{}, time.Time{}, err
	}
	defer file.Close()
	doc, err := decodeDocument(file)
	if err != nil {
		return Settings{}, time.Time{}, fmt.Errorf("decode stored site settings: %w", err)
	}
	return validateStoredDocument(doc)
}

func validateStoredDocument(doc document) (Settings, time.Time, error) {
	if doc.Schema != documentSchema || doc.Kind != documentKind {
		return Settings{}, time.Time{}, errors.New("stored site settings metadata is not supported")
	}
	normalized, fields, revisionMatches := normalizeDocumentSettings(doc)
	if len(fields) > 0 {
		return Settings{}, time.Time{}, fmt.Errorf("stored site settings are invalid: %v", fields)
	}
	if doc.Revision == "" || !revisionMatches {
		return Settings{}, time.Time{}, errors.New("stored site settings revision does not match its values")
	}
	return normalized, doc.UpdatedAt.UTC(), nil
}

func decodeDocument(reader io.Reader) (document, error) {
	limited := &io.LimitedReader{R: reader, N: maxDocumentBytes + 1}
	payload, err := io.ReadAll(limited)
	if err != nil {
		return document{}, err
	}
	if len(payload) == 0 {
		return document{}, errors.New("JSON document is empty")
	}
	if int64(len(payload)) > maxDocumentBytes {
		return document{}, fmt.Errorf("JSON document exceeds %d bytes", maxDocumentBytes)
	}
	if err := validateUniqueJSONObject(payload); err != nil {
		return document{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var doc document
	if err := decoder.Decode(&doc); err != nil {
		return document{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return document{}, errors.New("JSON document contains multiple values")
		}
		return document{}, err
	}
	// Match the settings decoder's field aliases when checking required fields.
	// RawMessage also distinguishes an explicit null from an absent field.
	var shape struct {
		Settings struct {
			Discussion   json.RawMessage `json:"discussion"`
			Writing      json.RawMessage `json:"writing"`
			Integrations json.RawMessage `json:"integrations"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(payload, &shape); err != nil {
		return document{}, err
	}
	var discussionShape struct {
		RequireLogin json.RawMessage `json:"require_login"`
	}
	if len(shape.Settings.Discussion) > 0 {
		if err := json.Unmarshal(shape.Settings.Discussion, &discussionShape); err != nil {
			return document{}, err
		}
	}
	doc.discussionPresent = len(shape.Settings.Discussion) > 0
	doc.discussionPolicyPresent = len(discussionShape.RequireLogin) > 0
	doc.integrationsPresent = len(shape.Settings.Integrations) > 0 && !bytes.Equal(bytes.TrimSpace(shape.Settings.Integrations), []byte("null"))
	var writingShape struct {
		AutosaveEnabled         json.RawMessage `json:"autosave_enabled"`
		AutosaveIntervalSeconds json.RawMessage `json:"autosave_interval_seconds"`
		AutosaveRetentionHours  json.RawMessage `json:"autosave_retention_hours"`
		PreviewTokenMinutes     json.RawMessage `json:"preview_token_minutes"`
		BrowserRecoveryEnabled  json.RawMessage `json:"browser_recovery_enabled"`
	}
	if len(shape.Settings.Writing) > 0 {
		if err := json.Unmarshal(shape.Settings.Writing, &writingShape); err != nil {
			return document{}, err
		}
	}
	writingFields := []json.RawMessage{writingShape.AutosaveEnabled, writingShape.AutosaveIntervalSeconds, writingShape.AutosaveRetentionHours, writingShape.PreviewTokenMinutes, writingShape.BrowserRecoveryEnabled}
	present := 0
	for _, raw := range writingFields {
		if len(raw) > 0 {
			present++
		}
	}
	doc.writingSafetyPresent = present == len(writingFields)
	return doc, nil
}

func writeDocument(path string, doc document) error {
	encoded, err := encodeDocument(doc)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := packagefs.MkdirAllNoSymlink(directory, 0o750); err != nil {
		return fmt.Errorf("create site settings directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".site-settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary site settings: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary site settings: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary site settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary site settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary site settings: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if directoryHandle, err := packagefs.OpenDirectoryNoSymlink(directory); err == nil {
			_ = directoryHandle.Sync()
			_ = directoryHandle.Close()
		}
	}
	return nil
}

func encodeDocument(doc document) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode site settings: %w", err)
	}
	if encoded.Len() > maxDocumentBytes {
		return nil, fmt.Errorf("site settings document exceeds %d bytes", maxDocumentBytes)
	}
	return encoded.Bytes(), nil
}

func replaceFile(temporaryPath, target string) error {
	return packagefs.ReplaceRegularFileNoSymlink(temporaryPath, target)
}

func settingsRevision(settings Settings) string {
	encoded, _ := json.Marshal(settings)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:8])
}

func normalizeDocumentSettings(doc document) (Settings, map[string]string, bool) {
	normalized, fields := Validate(doc.Settings)
	return normalized, fields, len(fields) == 0 && doc.discussionPresent && doc.discussionPolicyPresent && doc.writingSafetyPresent && doc.integrationsPresent && doc.Revision != "" && doc.Revision == settingsRevision(normalized)
}

func validateUniqueJSONObject(payload []byte) error {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("top-level JSON value must be an object")
	}
	return packagefs.ValidateUniqueJSONFor(payload, &document{})
}
