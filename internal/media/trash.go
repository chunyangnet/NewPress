package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"time"
)

// Trash retains the original object and identity, changing only its indexed
// availability. Application callers must check references before this write.
func (m *Manager) Trash(id, expectedVersion int64) (Item, error) {
	if m == nil || id <= 0 {
		return Item{}, ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.items[id]
	if !exists {
		return Item{}, ErrNotFound
	}
	if expectedVersion <= 0 || current.Version != expectedVersion {
		return current, ErrVersionConflict
	}
	if current.Status() == StatusTrash {
		return current, ErrTrashed
	}
	updated := current
	updated.TrashedAt = time.Now().UTC()
	return m.saveStateLocked(current, updated)
}

// Restore validates the retained bytes before making the original URL public.
func (m *Manager) Restore(ctx context.Context, id, expectedVersion int64) (Item, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil || id <= 0 {
		return Item{}, ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.items[id]
	if !exists {
		return Item{}, ErrNotFound
	}
	if expectedVersion <= 0 || current.Version != expectedVersion {
		return current, ErrVersionConflict
	}
	if current.Status() != StatusTrash {
		return current, ErrNotTrashed
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}
	for _, object := range current.Objects() {
		_, reader, err := m.openObjectLocked(object.descriptor())
		if err != nil {
			return current, err
		}
		hash := sha256.New()
		size, readErr := io.Copy(hash, &io.LimitedReader{R: contextReader{context: ctx, reader: reader}, N: object.SizeBytes + 1})
		closeErr := reader.Close()
		if readErr != nil {
			return current, readErr
		}
		if closeErr != nil {
			return current, closeErr
		}
		if size != object.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != object.ChecksumSHA256 {
			return current, ErrObjectCorrupt
		}
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}
	updated := current
	updated.TrashedAt = time.Time{}
	return m.saveStateLocked(current, updated)
}

func (m *Manager) saveStateLocked(current, updated Item) (Item, error) {
	updated.Version++
	updated.UpdatedAt = time.Now().UTC()
	previousUpdatedAt := m.updatedAt
	m.items[current.ID] = updated
	m.updatedAt = updated.UpdatedAt
	if err := m.persistLocked(); err != nil {
		m.items[current.ID] = current
		m.updatedAt = previousUpdatedAt
		return current, err
	}
	return updated, nil
}

func countStates(items []Item) map[string]int {
	counts := map[string]int{StatusActive: 0, StatusTrash: 0}
	for _, item := range items {
		counts[item.Status()]++
	}
	return counts
}
