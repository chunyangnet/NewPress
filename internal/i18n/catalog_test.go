package i18n

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func testCatalog(t *testing.T, fallback string, packs map[string]map[string]string) *Catalog {
	t.Helper()
	files := fstest.MapFS{}
	for locale, messages := range packs {
		data, err := json.Marshal(map[string]any{"schema": 1, "locale": locale, "messages": messages})
		if err != nil {
			t.Fatal(err)
		}
		files[locale+".json"] = &fstest.MapFile{Data: data}
	}
	catalog, err := LoadFS(files, ".", fallback)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestTranslationsFallbackAndIsolation(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t, "fr", map[string]map[string]string{
		"fr":      {"theme.only": "défaut", "navigation.home": "Accueil"},
		"en":      {"navigation.home": "Theme home", "theme.parent": "Parent", "empty": ""},
		"en-GB":   {"theme.exact": "Exact"},
		"zh-Hant": {"theme.script": "繁體"},
	})
	translator := NewTranslator("en-gb", catalog, Core())
	for key, want := range map[string]string{
		"theme.exact": "Exact", "theme.parent": "Parent", "theme.only": "défaut",
		"navigation.home": "Theme home", "navigation.account": "Account", "empty": "",
	} {
		got, ok := translator.Lookup(key)
		if !ok || got != want {
			t.Errorf("%s = %q, %v; want %q", key, got, ok, want)
		}
		if translator.Messages()[key] != want {
			t.Errorf("dictionary disagrees for %s", key)
		}
	}
	if translator.Locale() != "en-GB" || translator.DefaultLocale() != "fr" {
		t.Fatal("locale metadata is inconsistent")
	}
	if value, _ := translator.Text("missing.key"); value != "missing.key" {
		t.Fatal("missing key did not remain visible")
	}
	if _, found := translator.Lookup("missing.key"); found {
		t.Fatal("missing key was found")
	}
	if value, _ := NewTranslator("zh-TW", catalog, Core()).Lookup("theme.script"); value != "繁體" {
		t.Fatalf("script parent = %q", value)
	}
	if value, _ := NewTranslator("de", catalog, Core()).Lookup("navigation.home"); value != "Accueil" {
		t.Fatalf("theme default = %q", value)
	}
	if value, _ := NewTranslator("de", catalog, Core()).Lookup("navigation.account"); value != "账号" {
		t.Fatalf("core default = %q", value)
	}
	translator.Messages()["navigation.home"] = "mutated"
	locales := translator.Locales()
	locales[0] = "mutated"
	if value, _ := translator.Lookup("navigation.home"); value != "Theme home" || translator.Locales()[0] == "mutated" {
		t.Fatal("caller mutated shared catalog")
	}
}

func TestTranslationParametersAreLiteral(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t, "en", map[string]map[string]string{"en": {"greeting": "Hi {name}: {count}, {missing}; 100% {{.Secret}}"}})
	translator := NewTranslator("en", catalog)
	got, err := translator.Text("greeting", map[string]any{"name": "{count}<script>", "count": 3})
	if err != nil || got != "Hi {count}<script>: 3, {missing}; 100% {{.Secret}}" {
		t.Fatalf("interpolation = %q, %v", got, err)
	}
	if _, err := translator.Text("greeting", nil, nil); err == nil {
		t.Fatal("accepted multiple parameter dictionaries")
	}
	if _, err := translator.Text("greeting", map[string]any{"name": strings.Repeat("x", MaxFormattedBytes)}); err == nil {
		t.Fatal("accepted oversized parameter expansion")
	}
	repeated := testCatalog(t, "en", map[string]map[string]string{"en": {"repeat": strings.Repeat("{name}", 40)}})
	if _, err := NewTranslator("en", repeated).Text("repeat", map[string]any{"name": strings.Repeat("x", 2048)}); err == nil {
		t.Fatal("repeated placeholders bypassed formatted size limit")
	}
}

func TestLanguagePacksRejectInvalidDocuments(t *testing.T) {
	t.Parallel()
	valid := `{"schema":1,"locale":"en","messages":{"hello":"Hello"}}`
	cases := map[string]string{
		"duplicate":      `{"schema":1,"locale":"en","messages":{"hello":"A","hello":"B"}}`,
		"aliased schema": `{"schema":1,"SCHEMA":1,"locale":"en","messages":{"hello":"A"}}`,
		"unknown":        `{"schema":1,"locale":"en","messages":{"hello":"A"},"file":"secret"}`,
		"null":           `null`, "array": `[]`, "empty": `{}`,
		"schema":            strings.Replace(valid, `"schema":1`, `"schema":2`, 1),
		"mismatch":          strings.Replace(valid, `"locale":"en"`, `"locale":"fr"`, 1),
		"invalid locale":    strings.Replace(valid, `"locale":"en"`, `"locale":"../en"`, 1),
		"null messages":     `{"schema":1,"locale":"en","messages":null}`,
		"empty messages":    `{"schema":1,"locale":"en","messages":{}}`,
		"null message":      `{"schema":1,"locale":"en","messages":{"hello":null}}`,
		"number":            `{"schema":1,"locale":"en","messages":{"hello":2}}`,
		"nested":            `{"schema":1,"locale":"en","messages":{"hello":{"text":"Hello"}}}`,
		"bad key":           `{"schema":1,"locale":"en","messages":{"../hello":"Hello"}}`,
		"control":           `{"schema":1,"locale":"en","messages":{"hello":"\u0000"}}`,
		"surrogate":         `{"schema":1,"locale":"en","messages":{"hello":"\ud800"}}`,
		"invalid utf8":      strings.Replace(valid, "Hello", string([]byte{0xff}), 1),
		"trailing":          valid + `{}`,
		"oversized message": strings.Replace(valid, "Hello", strings.Repeat("x", MaxMessageBytes+1), 1),
		"oversized file":    valid + strings.Repeat(" ", MaxPackBytes),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFS(fstest.MapFS{"en.json": &fstest.MapFile{Data: []byte(data)}}, ".", "en"); err == nil {
				t.Fatal("accepted invalid language pack")
			}
		})
	}
	files := fstest.MapFS{"en.json": &fstest.MapFile{Data: []byte(valid)}}
	if _, err := LoadFS(files, ".", "fr"); err == nil {
		t.Fatal("accepted missing default language")
	}
	files["EN.json"] = &fstest.MapFile{Data: []byte(valid)}
	if _, err := LoadFS(files, ".", "en"); err == nil {
		t.Fatal("accepted duplicate canonical locale")
	}
	delete(files, "EN.json")
	files["README.md"] = &fstest.MapFile{Data: []byte("read me")}
	if _, err := LoadFS(files, ".", "en"); err == nil {
		t.Fatal("accepted non-pack file")
	}
}

func TestLanguagePackLimits(t *testing.T) {
	t.Parallel()
	messages := map[string]string{}
	for n := range MaxMessages + 1 {
		messages[fmt.Sprintf("key.%d", n)] = "value"
	}
	data, _ := json.Marshal(map[string]any{"schema": 1, "locale": "en", "messages": messages})
	if _, err := LoadFS(fstest.MapFS{"en.json": &fstest.MapFile{Data: data}}, ".", "en"); err == nil {
		t.Fatal("accepted too many messages")
	}
	files := fstest.MapFS{}
	for n := range MaxLocales + 1 {
		files[fmt.Sprintf("en-%03d.json", n)] = &fstest.MapFile{Data: data}
	}
	if _, err := LoadFS(files, ".", "en"); err == nil {
		t.Fatal("accepted too many packs")
	}
	catalog := &Catalog{packs: map[string]map[string]string{}, bytes: MaxCatalogBytes}
	if err := catalog.add("en.json", strings.NewReader(`{"schema":1,"locale":"en","messages":{"hello":"Hello"}}`)); err == nil {
		t.Fatal("accepted catalog exceeding total limit")
	}
}

func TestLanguageDirectoryAndLinks(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	packPath := filepath.Join(directory, "en.json")
	if err := os.WriteFile(packPath, []byte(`{"schema":1,"locale":"en","messages":{"hello":"你好😀\ud83d\ude00"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadDirectory(directory, "EN")
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := NewTranslator("en", catalog).Lookup("hello"); value != "你好😀😀" {
		t.Fatalf("Unicode changed: %q", value)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(directory, linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := LoadDirectory(linked, "en"); err == nil {
		t.Fatal("accepted linked directory")
	}
	if err := os.Symlink(packPath, filepath.Join(directory, "fr.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDirectory(directory, "en"); err == nil {
		t.Fatal("accepted linked pack")
	}
}

func TestCanonicalLocale(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{"EN-us": "en-US", "zh-hant": "zh-Hant", "iw": "he"} {
		got, err := CanonicalLocale(input)
		if err != nil || got != want {
			t.Errorf("%q = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"", "en_US", " en ", "../en", "x-private", "und", "zz", string([]byte{0xff}), strings.Repeat("en-", 20)} {
		if _, err := CanonicalLocale(input); err == nil {
			t.Errorf("accepted locale %q", input)
		}
	}
}
