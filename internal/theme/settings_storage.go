package theme

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

func loadSettingsDocument(directory string, manifest Manifest, schema *settingSchema) (map[string]any, time.Time, error) {
	values := schema.defaults()
	if directory == "" {
		return values, time.Time{}, nil
	}
	path := settingsFilePath(directory, manifest.Slug)
	file, _, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		// A Windows interruption can leave the last committed document under
		// the rollback name between the two replacement renames.
		file, _, err = packagefs.OpenRegularNoSymlink(path + ".bak")
		if errors.Is(err, os.ErrNotExist) {
			return values, time.Time{}, nil
		}
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("open theme settings: %w", err)
	}
	defer file.Close()
	var document settingsDocument
	if err := decodeJSONDocument(file, maxSettingsDocumentBytes, &document, true); err != nil {
		return nil, time.Time{}, fmt.Errorf("decode theme settings: %w", err)
	}
	return validateSettingsDocument(document, manifest, schema)
}

func validateSettingsDocument(document settingsDocument, manifest Manifest, schema *settingSchema) (map[string]any, time.Time, error) {
	if document.Schema != settingsDocumentSchema || document.Theme != manifest.Slug {
		return nil, time.Time{}, errors.New("stored theme settings metadata does not match the active theme")
	}
	normalized, errorsByField := schema.validate(document.Values)
	if len(errorsByField) > 0 {
		return nil, time.Time{}, fmt.Errorf("stored theme settings are invalid: %v", errorsByField)
	}
	return normalized, document.UpdatedAt, nil
}

func writeSettingsDocument(directory string, document settingsDocument) error {
	if err := packagefs.MkdirAllNoSymlink(directory, 0o750); err != nil {
		return fmt.Errorf("create theme settings directory: %w", err)
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode theme settings: %w", err)
	}
	if encoded.Len() > maxSettingsDocumentBytes {
		return fmt.Errorf("theme settings document exceeds %d bytes", maxSettingsDocumentBytes)
	}
	temporary, err := os.CreateTemp(directory, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary theme settings: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary theme settings: %w", err)
	}
	if _, err := temporary.Write(encoded.Bytes()); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary theme settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary theme settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary theme settings: %w", err)
	}
	target := settingsFilePath(directory, document.Theme)
	if err := packagefs.ReplaceRegularFileNoSymlink(temporaryPath, target); err != nil {
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

func decodeJSONDocument(reader io.Reader, limit int64, target any, disallowUnknown bool) error {
	limited := &io.LimitedReader{R: reader, N: limit + 1}
	payload, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		return errors.New("JSON document is empty")
	}
	if int64(len(payload)) > limit {
		return fmt.Errorf("JSON document exceeds %d bytes", limit)
	}
	if err := packagefs.ValidateUniqueJSONFor(payload, target); err != nil {
		return fmt.Errorf("validate JSON document: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if disallowUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("JSON document contains multiple values")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode trailing JSON data: %w", err)
	}
	return nil
}

func settingsFilePath(directory, slug string) string {
	return filepath.Join(directory, slug+".json")
}

func settingsRevision(manifest Manifest, values map[string]any) string {
	encoded, _ := json.Marshal(struct {
		Theme, Version, Effective string
		Values                    map[string]any
	}{manifest.Slug, manifest.Version, manifest.EffectiveRevision, values})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:8])
}
