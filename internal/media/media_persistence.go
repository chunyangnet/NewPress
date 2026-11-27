package media

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
	"sort"

	"newpress.local/newpress/internal/packagefs"
)

func (m *Manager) persistLocked() error {
	if m.root == "" {
		return nil
	}
	items := m.sortedItemsLocked()
	doc := document{
		Schema: documentSchema, Kind: documentKind, NextID: m.nextID,
		UpdatedAt: m.updatedAt.UTC(), Items: items,
	}
	doc.Revision = libraryRevision(doc.NextID, doc.Items)
	return writeDocument(m.indexPath, doc)
}

func (m *Manager) sortedItemsLocked() []Item {
	items := make([]Item, 0, len(m.items))
	for _, item := range m.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items
}

func (m *Manager) loadStored(doc document) error {
	if doc.Schema != documentSchema || doc.Kind != documentKind || doc.NextID <= 0 {
		return errors.New("stored media library metadata is not supported")
	}
	if doc.Revision == "" || doc.Revision != libraryRevision(doc.NextID, doc.Items) {
		return errors.New("stored media library revision does not match its values")
	}
	maximumID := int64(0)
	for _, item := range doc.Items {
		if err := validateStoredItem(item); err != nil {
			return fmt.Errorf("invalid stored media item %d: %w", item.ID, err)
		}
		if _, exists := m.items[item.ID]; exists {
			return fmt.Errorf("duplicate stored media id %d", item.ID)
		}
		if _, exists := m.checksumIndex[checksumKey(item.ChecksumSHA256, item.UploadSource)]; exists {
			return fmt.Errorf("duplicate stored media checksum %s", item.ChecksumSHA256)
		}
		for _, object := range item.Objects() {
			target, err := m.objectPath(object.ObjectKey)
			if err != nil {
				return err
			}
			file, info, err := packagefs.OpenRegularNoSymlink(target)
			if err == nil {
				_ = file.Close()
			}
			if err != nil || !info.Mode().IsRegular() || info.Size() != object.SizeBytes {
				if err == nil {
					err = errors.New("object is not a matching regular file")
				}
				return fmt.Errorf("verify media object %q: %w", object.ObjectKey, err)
			}
		}
		m.items[item.ID] = item
		m.checksumIndex[checksumKey(item.ChecksumSHA256, item.UploadSource)] = item.ID
		maximumID = max(maximumID, item.ID)
	}
	if doc.NextID <= maximumID {
		return errors.New("stored media next_id does not exceed existing ids")
	}
	m.nextID = doc.NextID
	m.updatedAt = doc.UpdatedAt.UTC()
	return nil
}

func libraryRevision(nextID int64, items []Item) string {
	payload, _ := json.Marshal(struct {
		NextID int64  `json:"next_id"`
		Items  []Item `json:"items"`
	}{NextID: nextID, Items: items})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:8])
}

func loadDocument(path string) (document, error) {
	file, _, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		file, _, err = packagefs.OpenRegularNoSymlink(path + ".bak")
	}
	if err != nil {
		return document{}, err
	}
	defer file.Close()
	limited := &io.LimitedReader{R: file, N: maxDocumentBytes + 1}
	payload, err := io.ReadAll(limited)
	if err != nil {
		return document{}, fmt.Errorf("read stored media library: %w", err)
	}
	if len(payload) == 0 || int64(len(payload)) > maxDocumentBytes {
		return document{}, errors.New("stored media library has an invalid size")
	}
	if err := packagefs.ValidateUniqueJSONFor(payload, &document{}); err != nil {
		return document{}, fmt.Errorf("decode stored media library: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var doc document
	if err := decoder.Decode(&doc); err != nil {
		return document{}, fmt.Errorf("decode stored media library: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return document{}, errors.New("stored media library contains trailing JSON")
	}
	return doc, nil
}

func writeDocument(path string, doc document) error {
	directory := filepath.Dir(path)
	if err := packagefs.MkdirAllNoSymlink(directory, 0o750); err != nil {
		return fmt.Errorf("create media library directory: %w", err)
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return fmt.Errorf("encode media library: %w", err)
	}
	if encoded.Len() > maxDocumentBytes {
		return fmt.Errorf("media library exceeds %d bytes", maxDocumentBytes)
	}
	temporary, err := os.CreateTemp(directory, ".media-library-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary media library: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary media library: %w", err)
	}
	if _, err := temporary.Write(encoded.Bytes()); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary media library: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary media library: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary media library: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if handle, err := packagefs.OpenDirectoryNoSymlink(directory); err == nil {
			_ = handle.Sync()
			_ = handle.Close()
		}
	}
	return nil
}

func replaceFile(temporaryPath, target string) error {
	return packagefs.ReplaceRegularFileNoSymlink(temporaryPath, target)
}
