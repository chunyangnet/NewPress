package theme

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"

	"newpress.local/newpress/internal/extension"
)

// Called under opMu. Bind operation tokens to both sides of the dependency
// relationship, so a queued operation cannot silently cross a package update.
func (m *Manager) dependencyRevision(info Manifest, own string) (string, error) {
	slugs, err := m.dependents(info.Slug)
	if err != nil {
		return "", err
	}
	if info.Parent != nil {
		slugs = append(slugs, info.Parent.Slug)
	}
	if len(slugs) == 0 {
		return own, nil
	}
	type dependency struct {
		Manifest   Manifest
		Revision   string
		Generation uint64
	}
	entries := make([]dependency, 0, len(slugs))
	for _, slug := range slugs {
		manifest, err := readManifest(filepath.Join(m.root, slug, "theme.json"))
		if err != nil {
			return "", err
		}
		state, err := extension.State(filepath.Join(m.root, slug), manifest.Version)
		if err != nil {
			return "", err
		}
		entries = append(entries, dependency{manifest, state.Revision, m.generation[slug]})
	}
	data, err := json.Marshal(struct {
		Own          string
		Dependencies []dependency
	}{own, entries})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
