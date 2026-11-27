package media

import (
	"sort"
)

// StorageObject is an immutable, indexed object reference for backup/sync.
// It conveys no arbitrary filesystem access to storage adapters.
type StorageObject struct {
	ItemID       int64
	Filename     string
	Trashed      bool
	UploadSource string
	ImageObject
}

func (m *Manager) StorageInventory() []StorageObject {
	m.mu.RLock()
	defer m.mu.RUnlock()
	objects := []StorageObject{}
	for _, item := range m.items {
		for _, object := range item.Objects() {
			objects = append(objects, StorageObject{ItemID: item.ID, Filename: item.Filename, Trashed: item.Status() == StatusTrash, UploadSource: item.UploadSource, ImageObject: object})
		}
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].ItemID != objects[j].ItemID {
			return objects[i].ItemID < objects[j].ItemID
		}
		return objects[i].ObjectKey < objects[j].ObjectKey
	})
	return objects
}

// HasStorageObject checks a single indexed object without sorting or copying
// the entire media library for each cleanup job.
func (m *Manager) HasStorageObject(id int64, key string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.items[id]
	if !ok || item.Status() == StatusTrash {
		return false
	}
	for _, object := range item.Objects() {
		if object.ObjectKey == key {
			return true
		}
	}
	return false
}

// OpenStorageObject rechecks membership under the media lock. Sync jobs cannot
// read stale derivatives, deleted objects or paths supplied by a remote server.
func (m *Manager) OpenStorageObject(id int64, key string) (Item, ReadSeekCloser, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.items[id]
	if !ok || item.Status() == StatusTrash {
		return Item{}, nil, ErrNotFound
	}
	for _, obj := range item.Objects() {
		if obj.ObjectKey == key {
			d := obj.descriptor()
			d.ID = id
			d.Filename = item.Filename
			return m.openObjectLocked(d)
		}
	}
	return Item{}, nil, ErrNotFound
}
