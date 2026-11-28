package media

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) Upload(ctx context.Context, reader io.Reader, input UploadInput, maxBytes int64) (Item, error) {
	if m == nil || reader == nil {
		return Item{}, ErrInvalidInput
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	normalized, err := normalizeUploadInput(input)
	if err != nil {
		return Item{}, err
	}
	if maxBytes <= 0 {
		maxBytes = defaultUploadLimit
	}
	staged, err := m.stageUpload(ctx, reader, maxBytes)
	if err != nil {
		return Item{}, err
	}
	defer staged.discard()

	mediaType, extension, kind, err := classify(staged.prefix)
	if err != nil {
		return Item{}, err
	}
	width, height, err := staged.dimensions(mediaType)
	if err != nil {
		return Item{}, err
	}
	filename := normalizedFilename(normalized.Filename, extension)
	title := normalized.Title
	if title == "" {
		title = strings.TrimSuffix(filename, filepath.Ext(filename))
	}
	if err := validateText("title", title, true, maxTitleRunes); err != nil {
		return Item{}, err
	}
	checksum := hex.EncodeToString(staged.digest[:])
	indexKey := checksumKey(checksum, normalized.UploadSource)

	m.mu.Lock()
	defer m.mu.Unlock()
	// Staging and waiting for the library lock may outlive the caller.
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	if existingID, ok := m.checksumIndex[indexKey]; ok {
		existing := m.items[existingID]
		if existing.Status() == StatusTrash {
			return existing, ErrDuplicateTrash
		}
		if err := m.verifyDuplicateObjectLocked(ctx, staged, existing); err != nil {
			return Item{}, err
		}
		return existing, ErrDuplicate
	}
	now := time.Now().UTC()
	id := m.nextID
	if id <= 0 {
		id = 1
	}
	objectKey := filepath.ToSlash(filepath.Join("objects", checksum[:2], checksum+"-"+normalized.UploadSource+extension))
	item := Item{
		ID: id, Version: 1, Storage: storageName(m.root), ObjectKey: objectKey,
		PublicURL: "/media/" + fmt.Sprintf("%d", id) + "/" + url.PathEscape(filename),
		Filename:  filename, Title: title, AltText: normalized.AltText, Caption: normalized.Caption,
		MIMEType: mediaType, Kind: kind, SizeBytes: staged.size, Width: width, Height: height,
		ChecksumSHA256: checksum, UploadedBy: normalized.UploadedBy, CreatedAt: now, UpdatedAt: now,
		UploadSource: normalized.UploadSource,
	}
	createdObject, err := m.commitObject(ctx, staged, item)
	if err != nil {
		return Item{}, err
	}
	previousNextID, previousUpdatedAt := m.nextID, m.updatedAt
	m.items[id] = item
	m.checksumIndex[indexKey] = id
	m.nextID = id + 1
	m.updatedAt = now
	if err := m.persistLocked(); err != nil {
		delete(m.items, id)
		delete(m.checksumIndex, indexKey)
		m.nextID, m.updatedAt = previousNextID, previousUpdatedAt
		if createdObject {
			_ = m.removeObjectLocked(item)
		}
		return Item{}, err
	}
	return item, nil
}

func (m *Manager) Update(id, expectedVersion int64, input UpdateInput) (Item, error) {
	if m == nil || id <= 0 {
		return Item{}, ErrNotFound
	}
	normalized, err := normalizeUpdateInput(input)
	if err != nil {
		return Item{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.items[id]
	if !ok {
		return Item{}, ErrNotFound
	}
	if expectedVersion <= 0 || current.Version != expectedVersion {
		return current, ErrVersionConflict
	}
	updated := current
	updated.Title = normalized.Title
	updated.AltText = normalized.AltText
	updated.Caption = normalized.Caption
	updated.Version++
	updated.UpdatedAt = time.Now().UTC()
	previousUpdatedAt := m.updatedAt
	m.items[id] = updated
	m.updatedAt = updated.UpdatedAt
	if err := m.persistLocked(); err != nil {
		m.items[id] = current
		m.updatedAt = previousUpdatedAt
		return Item{}, err
	}
	return updated, nil
}

func (m *Manager) Delete(id, expectedVersion int64) error {
	if m == nil || id <= 0 {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[id]
	if !ok {
		return ErrNotFound
	}
	if expectedVersion <= 0 || item.Version != expectedVersion {
		return ErrVersionConflict
	}
	if item.Status() != StatusTrash {
		return ErrNotTrashed
	}
	rollbacks := []func(bool){}
	for _, object := range item.Objects() {
		rollback, err := m.stageObjectDeletionLocked(object.descriptor())
		if err != nil {
			for i := len(rollbacks) - 1; i >= 0; i-- {
				rollbacks[i](false)
			}
			return err
		}
		rollbacks = append(rollbacks, rollback)
	}
	previousUpdatedAt := m.updatedAt
	delete(m.items, id)
	delete(m.checksumIndex, checksumKey(item.ChecksumSHA256, item.UploadSource))
	m.updatedAt = time.Now().UTC()
	if err := m.persistLocked(); err != nil {
		m.items[id] = item
		m.checksumIndex[checksumKey(item.ChecksumSHA256, item.UploadSource)] = id
		m.updatedAt = previousUpdatedAt
		for i := len(rollbacks) - 1; i >= 0; i-- {
			rollbacks[i](false)
		}
		return err
	}
	for _, rollback := range rollbacks {
		rollback(true)
	}
	return nil
}
