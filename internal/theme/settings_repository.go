package theme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SettingsRepository stores one document per theme. Versions start at 1 and
// advance on every successful compare-and-swap, including identical values.
// A missing document has version 0. Repository failures must not fall back to
// local files: those files are only an import source for an empty repository.
type SettingsRepository interface {
	Load(context.Context, string) ([]byte, int64, bool, error)
	Create(context.Context, string, []byte) (bool, error)
	CompareAndSwap(context.Context, string, int64, []byte) (bool, error)
}

const settingsRepositoryTimeout = 10 * time.Second

func WithSettingsRepository(repository SettingsRepository) ManagerOption {
	return func(manager *Manager) {
		manager.settingsRepository = repository
	}
}

// Caller holds opMu. Upgrade/rollback probes use initialize=false so merely
// validating a candidate package never rewrites site settings.
func (m *Manager) loadSettings(manifest Manifest, schema *settingSchema, initialize bool) (map[string]any, time.Time, int64, error) {
	if m.settingsRepository == nil {
		values, updatedAt, err := loadSettingsDocument(m.settingsDir, manifest, schema)
		return values, updatedAt, 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), settingsRepositoryTimeout)
	defer cancel()
	payload, version, found, err := m.settingsRepository.Load(ctx, manifest.Slug)
	if err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("load persistent theme settings: %w", err)
	}
	if found {
		return decodePersistentSettings(payload, version, manifest, schema)
	}
	values, updatedAt, err := loadSettingsDocument(m.settingsDir, manifest, schema)
	if err != nil || !initialize {
		return values, updatedAt, 0, err
	}
	payload, err = encodePersistentSettings(settingsDocument{
		Schema: settingsDocumentSchema, Theme: manifest.Slug, ThemeVersion: manifest.Version,
		UpdatedAt: updatedAt, Values: values,
	})
	if err != nil {
		return nil, time.Time{}, 0, err
	}
	created, err := m.settingsRepository.Create(ctx, manifest.Slug, payload)
	if err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("initialize persistent theme settings: %w", err)
	}
	if created {
		return values, updatedAt, 1, nil
	}
	// Another instance won initialization. Use its complete document rather
	// than publishing the losing instance's local import or default values.
	payload, version, found, err = m.settingsRepository.Load(ctx, manifest.Slug)
	if err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("reload persistent theme settings: %w", err)
	}
	if !found {
		return nil, time.Time{}, 0, errors.New("persistent theme settings disappeared during initialization")
	}
	return decodePersistentSettings(payload, version, manifest, schema)
}

func encodePersistentSettings(document settingsDocument) ([]byte, error) {
	payload, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode persistent theme settings: %w", err)
	}
	if len(payload) > maxSettingsDocumentBytes {
		return nil, fmt.Errorf("theme settings document exceeds %d bytes", maxSettingsDocumentBytes)
	}
	return payload, nil
}

func decodePersistentSettings(payload []byte, version int64, manifest Manifest, schema *settingSchema) (map[string]any, time.Time, int64, error) {
	if version < 1 {
		return nil, time.Time{}, 0, errors.New("persistent theme settings version is invalid")
	}
	var document settingsDocument
	if err := decodeJSONDocument(bytes.NewReader(payload), maxSettingsDocumentBytes, &document, true); err != nil {
		return nil, time.Time{}, 0, fmt.Errorf("decode persistent theme settings: %w", err)
	}
	if document.Values == nil {
		return nil, time.Time{}, 0, errors.New("persistent theme settings values must be an object")
	}
	values, updatedAt, err := validateSettingsDocument(document, manifest, schema)
	return values, updatedAt, version, err
}

// Caller holds opMu. On a rejected compare-and-swap refresh the visible
// snapshot so a fresh form can retry without requiring a process restart.
func (m *Manager) persistSettings(document settingsDocument, version int64, manifest Manifest, schema *settingSchema) error {
	payload, err := encodePersistentSettings(document)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), settingsRepositoryTimeout)
	defer cancel()
	var saved bool
	if version == 0 {
		saved, err = m.settingsRepository.Create(ctx, manifest.Slug, payload)
	} else {
		saved, err = m.settingsRepository.CompareAndSwap(ctx, manifest.Slug, version, payload)
	}
	if err != nil {
		return fmt.Errorf("save persistent theme settings: %w", err)
	}
	if saved {
		return nil
	}
	payload, currentVersion, found, err := m.settingsRepository.Load(ctx, manifest.Slug)
	if err != nil {
		return fmt.Errorf("refresh conflicting theme settings: %w", err)
	}
	if !found {
		return errors.New("persistent theme settings disappeared while saving")
	}
	values, updatedAt, currentVersion, err := decodePersistentSettings(payload, currentVersion, manifest, schema)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.settings, m.settingsUpdatedAt, m.settingsStoreVersion = values, updatedAt, currentVersion
	m.mu.Unlock()
	return ErrSettingsRevisionConflict
}
