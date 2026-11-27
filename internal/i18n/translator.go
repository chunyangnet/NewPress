package i18n

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/language"
)

var placeholderPattern = regexp.MustCompile(`\{[A-Za-z][A-Za-z0-9_]{0,63}\}`)

// Translator captures one locale and immutable catalogs for a render/request.
// At each language level earlier catalogs take precedence. Requested language
// parents precede all defaults; defaults follow catalog order. Parent uses
// language.Tag.Parent, preserving distinctions such as zh-Hant vs zh-Hans.
type Translator struct {
	locale        string
	defaultLocale string
	locales       []string
	layers        []map[string]string
}

func NewTranslator(locale string, catalogs ...*Catalog) Translator {
	canonical, err := CanonicalLocale(locale)
	if err != nil {
		canonical = DefaultLocale
	}
	t := Translator{locale: canonical, defaultLocale: DefaultLocale, locales: []string{}}
	seenLocales := map[string]bool{}
	for _, catalog := range catalogs {
		if catalog == nil {
			continue
		}
		if len(seenLocales) == 0 {
			t.defaultLocale = catalog.defaultLocale
		}
		for name := range catalog.packs {
			seenLocales[name] = true
		}
	}
	for name := range seenLocales {
		t.locales = append(t.locales, name)
	}
	slices.Sort(t.locales)
	seen := map[string]bool{}
	appendLanguage := func(value string) {
		tag, _ := language.Parse(value)
		for tag != language.Und {
			name := tag.String()
			if !seen[name] {
				seen[name] = true
				for _, catalog := range catalogs {
					if catalog != nil && catalog.packs[name] != nil {
						t.layers = append(t.layers, catalog.packs[name])
					}
				}
			}
			tag = tag.Parent()
		}
	}
	appendLanguage(canonical)
	for _, catalog := range catalogs {
		if catalog != nil {
			appendLanguage(catalog.defaultLocale)
		}
	}
	return t
}

func (t Translator) Locale() string        { return t.locale }
func (t Translator) DefaultLocale() string { return t.defaultLocale }
func (t Translator) Locales() []string     { return slices.Clone(t.locales) }

func (t Translator) Lookup(key string) (string, bool) {
	for _, layer := range t.layers {
		if value, ok := layer[key]; ok {
			return value, true
		}
	}
	return "", false
}

// Text returns plain text. Named replacements are literal and are never parsed
// again as placeholders, Go templates or HTML. Missing names remain visible.
func (t Translator) Text(key string, parameters ...map[string]any) (string, error) {
	if len(parameters) > 1 {
		return "", fmt.Errorf("t accepts at most one parameter dictionary")
	}
	value, ok := t.Lookup(key)
	if !ok {
		return key, nil
	}
	if len(parameters) == 0 {
		return value, nil
	}
	var output strings.Builder
	write := func(text string) error {
		if len(text) > MaxFormattedBytes-output.Len() {
			return fmt.Errorf("formatted translation exceeds %d bytes", MaxFormattedBytes)
		}
		output.WriteString(text)
		return nil
	}
	last := 0
	for _, match := range placeholderPattern.FindAllStringIndex(value, -1) {
		if err := write(value[last:match[0]]); err != nil {
			return "", err
		}
		token := value[match[0]:match[1]]
		if replacement, ok := parameters[0][token[1:len(token)-1]]; ok {
			token = fmt.Sprint(replacement)
		}
		if err := write(token); err != nil {
			return "", err
		}
		last = match[1]
	}
	if err := write(value[last:]); err != nil {
		return "", err
	}
	return output.String(), nil
}

// Messages returns an independent copy of the effective dictionary.
func (t Translator) Messages() map[string]string {
	messages := map[string]string{}
	for i := len(t.layers) - 1; i >= 0; i-- {
		maps.Copy(messages, t.layers[i])
	}
	return messages
}
