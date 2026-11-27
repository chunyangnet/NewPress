package i18n

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"newpress.local/newpress/internal/packagefs"
)

// LoadOverlayDirectories merges by locale/key before applying locale fallback.
// A child may contain only a partial dictionary, including no default pack.
func LoadOverlayDirectories(parent, child, defaultLocale string) (*Catalog, error) {
	requireDefault := defaultLocale != ""
	if defaultLocale == "" {
		defaultLocale = DefaultLocale
	}
	locale, err := CanonicalLocale(defaultLocale)
	if err != nil {
		return nil, err
	}
	result := &Catalog{defaultLocale: locale, packs: map[string]map[string]string{}}
	for _, directory := range []string{parent, child} {
		d, err := packagefs.OpenDirectoryNoSymlink(directory)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries, err := d.ReadDir(MaxLocales + 1)
		d.Close()
		if err != nil && err != io.EOF {
			return nil, err
		}
		if len(entries) > MaxLocales {
			return nil, fmt.Errorf("too many language packs")
		}
		layer := &Catalog{packs: map[string]map[string]string{}}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".json" {
				return nil, fmt.Errorf("language pack must be regular JSON")
			}
			f, _, err := packagefs.OpenRegularNoSymlink(filepath.Join(directory, entry.Name()))
			if err != nil {
				return nil, err
			}
			before := layer.bytes
			err = layer.add(entry.Name(), f)
			f.Close()
			if err != nil {
				return nil, err
			}
			key, err := CanonicalLocale(strings.TrimSuffix(entry.Name(), ".json"))
			if err != nil {
				return nil, err
			}
			if result.packs[key] == nil {
				result.packs[key] = map[string]string{}
			}
			maps.Copy(result.packs[key], layer.packs[key])
			result.bytes += layer.bytes - before
			if len(result.packs) > MaxLocales || len(result.packs[key]) > MaxMessages || result.bytes > MaxCatalogBytes {
				return nil, fmt.Errorf("combined language packs exceed limits")
			}
		}
	}
	if len(result.packs) == 0 && !requireDefault {
		return nil, nil
	}
	if result.packs[locale] == nil {
		return nil, fmt.Errorf("default language pack %s missing", locale)
	}
	return result, nil
}
