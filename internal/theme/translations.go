package theme

import (
	"fmt"
	"os"
	"path/filepath"

	"newpress.local/newpress/internal/i18n"
	"newpress.local/newpress/internal/packagefs"
)

// LocaleProvider supplies the request's captured site locale. Locale is bound
// once per render, including partials whose dot is a different object.
type LocaleProvider interface{ SiteLocale() string }

func loadTranslations(directory string, manifest Manifest) (*i18n.Catalog, error) {
	location := filepath.Join(directory, "locales")
	if _, err := packagefs.LstatNoSymlink(location); os.IsNotExist(err) && manifest.DefaultLocale == "" {
		return nil, nil // Existing themes may omit language packs entirely.
	} else if err != nil {
		return nil, fmt.Errorf("inspect theme language packs: %w", err)
	}
	catalog, err := i18n.LoadDirectory(location, manifest.DefaultLocale)
	if err != nil {
		return nil, fmt.Errorf("load theme language packs: %w", err)
	}
	return catalog, nil
}

type TranslationSnapshot struct {
	Theme      string
	Translator i18n.Translator
}

// Translations captures the selected theme and its dictionary together. No
// filesystem I/O occurs during lookups or rendering.
func (m *Manager) Translations(locale string) TranslationSnapshot {
	m.mu.RLock()
	slug, catalog := m.info.Slug, m.translations
	m.mu.RUnlock()
	return TranslationSnapshot{Theme: slug, Translator: i18n.NewTranslator(locale, catalog, i18n.Core())}
}
