// Package i18n loads immutable, plain-text language packs for presentation.
package i18n

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/language"
	"newpress.local/newpress/internal/packagefs"
)

const (
	DefaultLocale     = "zh-CN"
	MaxLocales        = 32
	MaxPackBytes      = 512 << 10
	MaxCatalogBytes   = 8 << 20
	MaxMessages       = 4096
	MaxMessageBytes   = 8192
	MaxFormattedBytes = 64 << 10
)

var (
	localePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$`)
	keyPattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
)

// CanonicalLocale accepts the site's bounded language-tag syntax. Invalid tags
// are errors, never paths or a best-effort language guessed by the parser.
func CanonicalLocale(value string) (string, error) {
	if len(value) > 35 || !localePattern.MatchString(value) {
		return "", fmt.Errorf("invalid locale %q", value)
	}
	tag, err := language.Parse(value)
	if err != nil || tag == language.Und {
		return "", fmt.Errorf("invalid locale %q", value)
	}
	return tag.String(), nil
}

func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// Catalog is immutable after loading and may be shared by concurrent renders.
// Its maps are private; callers receive copies through Translator.Messages.
type Catalog struct {
	defaultLocale string
	packs         map[string]map[string]string
	bytes         int
}

type pack struct {
	Schema   int                `json:"schema"`
	Locale   string             `json:"locale"`
	Messages map[string]*string `json:"messages"`
}

// LoadFS loads a trusted filesystem, such as embed.FS. For installed theme
// directories, use LoadDirectory so links and non-regular files are rejected.
func LoadFS(files fs.FS, directory, defaultLocale string) (*Catalog, error) {
	entries, err := fs.ReadDir(files, directory)
	if err != nil {
		return nil, err
	}
	return load(entries, defaultLocale, func(name string) (io.ReadCloser, error) {
		return files.Open(path.Join(directory, name))
	})
}

// LoadDirectory opens only regular files below a real directory. A missing
// directory is an error; theme packages decide whether their packs are optional.
func LoadDirectory(directory, defaultLocale string) (*Catalog, error) {
	dir, err := packagefs.OpenDirectoryNoSymlink(directory)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(MaxLocales + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return load(entries, defaultLocale, func(name string) (io.ReadCloser, error) {
		file, _, err := packagefs.OpenRegularNoSymlink(filepath.Join(directory, name))
		return file, err
	})
}

func load(entries []fs.DirEntry, defaultLocale string, open func(string) (io.ReadCloser, error)) (*Catalog, error) {
	if len(entries) == 0 || len(entries) > MaxLocales {
		return nil, fmt.Errorf("language directory must contain 1 to %d packs", MaxLocales)
	}
	if defaultLocale == "" {
		defaultLocale = DefaultLocale
	}
	canonical, err := CanonicalLocale(defaultLocale)
	if err != nil {
		return nil, err
	}
	catalog := &Catalog{defaultLocale: canonical, packs: make(map[string]map[string]string)}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || path.Ext(name) != ".json" {
			return nil, fmt.Errorf("language pack %q must be a regular JSON file", name)
		}
		file, err := open(name)
		if err != nil {
			return nil, fmt.Errorf("open language pack %s: %w", name, err)
		}
		loadErr := catalog.add(name, file)
		closeErr := file.Close()
		if loadErr != nil {
			return nil, fmt.Errorf("load language pack %s: %w", name, loadErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if catalog.packs[catalog.defaultLocale] == nil {
		return nil, fmt.Errorf("default language pack %s is missing", catalog.defaultLocale)
	}
	return catalog, nil
}

func (c *Catalog) add(name string, reader io.Reader) error {
	locale, err := CanonicalLocale(strings.TrimSuffix(name, ".json"))
	if err != nil {
		return err
	}
	if c.packs[locale] != nil {
		return fmt.Errorf("duplicate language pack %s", locale)
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxPackBytes+1))
	if err != nil {
		return err
	}
	if len(data) > MaxPackBytes || c.bytes+len(data) > MaxCatalogBytes {
		return fmt.Errorf("language pack or catalog exceeds its size limit")
	}
	document, err := packagefs.DecodeJSON[pack](bytes.NewReader(data), MaxPackBytes, name)
	if err != nil {
		return err
	}
	declared, err := CanonicalLocale(document.Locale)
	if err != nil || declared != locale || document.Schema != 1 {
		return fmt.Errorf("language pack requires schema=1 and a locale matching its filename")
	}
	if len(document.Messages) == 0 || len(document.Messages) > MaxMessages {
		return fmt.Errorf("language pack must contain 1 to %d messages", MaxMessages)
	}
	messages := make(map[string]string, len(document.Messages))
	for key, value := range document.Messages {
		if !ValidKey(key) || value == nil || len(*value) > MaxMessageBytes {
			return fmt.Errorf("invalid language message %q", key)
		}
		for _, r := range *value {
			if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
				return fmt.Errorf("language message %q contains control characters", key)
			}
		}
		messages[key] = *value
	}
	c.packs[locale] = messages
	c.bytes += len(data)
	return nil
}
