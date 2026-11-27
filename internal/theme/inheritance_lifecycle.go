package theme

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func parentSlug(m Manifest) string {
	if m.Parent != nil {
		return m.Parent.Slug
	}
	return ""
}

// Caller holds opMu. Fail closed when an installed package cannot be inspected.
func (m *Manager) dependents(parent string) ([]string, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("linked theme directory %s is not allowed", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		info, err := readManifest(filepath.Join(m.root, entry.Name(), "theme.json"))
		if err != nil {
			return nil, fmt.Errorf("inspect theme %s: %w", entry.Name(), err)
		}
		if parentSlug(info) == parent {
			result = append(result, entry.Name())
		}
	}
	return result, nil
}

// LoadSource validates an explicit source against a local dependency root.
// It never writes a selection or settings document.
func (m *Manager) LoadSource(source string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	manifest, err := readManifest(filepath.Join(source, "theme.json"))
	if err != nil {
		return err
	}
	prepared, err := prepareTheme(m.root, manifest.Slug, map[string]string{manifest.Slug: source})
	if err != nil {
		return err
	}
	settings, at, version, err := m.loadSettings(prepared.info, prepared.schema, false)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active, m.info = manifest.Slug, prepared.info
	m.templates, m.settingsSchema, m.translations = prepared.templates, prepared.schema, prepared.translations
	m.assetRevision, m.assetHashes, m.assetOwners = prepared.assets.revision, prepared.assets.files, prepared.owners
	m.settings, m.settingsUpdatedAt, m.settingsStoreVersion = settings, at, version
	return nil
}
