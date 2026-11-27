package theme

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"newpress.local/newpress/internal/presentation"
)

type localeTestPage struct{ Locale, Name string }

func (p localeTestPage) SiteLocale() string { return p.Locale }

func translationArchive(t *testing.T, manifest Manifest, home string, packs map[string]string) (Release, string) {
	t.Helper()
	release, original := themeOTAArchive(t, manifest, otaSettings, home)
	source, err := zip.OpenReader(original)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	for _, file := range source.File {
		if err := archive.Copy(file); err != nil {
			t.Fatal(err)
		}
	}
	for locale, body := range packs {
		file, err := archive.Create("locales/" + locale + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "translated.zip")
	if err := os.WriteFile(filename, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data.Bytes())
	release.SHA256, release.Size = hex.EncodeToString(sum[:]), int64(data.Len())
	return release, filename
}

func translationPack(t *testing.T, locale string, messages map[string]string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"schema": 1, "locale": locale, "messages": messages})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestThemeTranslationsRenderAndSwitchAtomically(t *testing.T) {
	t.Parallel()
	m, manifest := themeOTAFixture(t)
	target, err := m.CaptureUpdate(manifest.Slug, "", "")
	if err != nil {
		t.Fatal(err)
	}
	manifest.DefaultLocale, manifest.Version = "en", "1.1.0"
	packs := map[string]string{"en": translationPack(t, "en", map[string]string{"greeting": "Hello {name}", "navigation.home": "Theme home"})}
	release, archive := translationArchive(t, manifest, `{{define "content"}}{{t "greeting" (dict "name" .Name)}}|{{t "navigation.account"}}|{{t "missing"}}{{end}}`, packs)
	if err := m.UpdatePackage(t.Context(), archive, release, target, nil); err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	if err := m.Render(&rendered, "home", localeTestPage{Locale: "en-US", Name: "<script>{name}</script>"}); err != nil {
		t.Fatal(err)
	}
	if got := rendered.String(); got != "Hello &lt;script&gt;{name}&lt;/script&gt;|Account|missing" {
		t.Fatalf("rendered %q", got)
	}
	captured := m.Translations("en-US")
	if got, _ := captured.Translator.Lookup("navigation.home"); got != "Theme home" {
		t.Fatal(got)
	}

	// A bad update fails before replacing the on-disk package or live snapshot.
	target, err = m.CaptureUpdate(manifest.Slug, "", "")
	if err != nil {
		t.Fatal(err)
	}
	manifest.Version = "1.2.0"
	badRelease, badArchive := translationArchive(t, manifest, `{{define "content"}}bad{{end}}`, map[string]string{"en": `{"schema":1,"locale":"en","messages":{"greeting":null}}`})
	if err := m.UpdatePackage(t.Context(), badArchive, badRelease, target, nil); err == nil {
		t.Fatal("accepted broken language pack")
	}
	if m.Active().Version != "1.1.0" {
		t.Fatal("failed update changed active version")
	}
	state, err := m.VersionState(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(t.Context(), manifest.Slug, "1.1.0", state.Revision, "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Translations("en-US").Translator.Lookup("navigation.home"); got != "Home" {
		t.Fatalf("rollback retained removed dictionary: %q", got)
	}
	if got, _ := captured.Translator.Lookup("navigation.home"); got != "Theme home" {
		t.Fatal("existing snapshot changed during rollback")
	}

	// Installing and activating also captures the new theme dictionary.
	manifest.Slug, manifest.Version = "translated-theme", "1.0.0"
	_, archive = translationArchive(t, manifest, `{{define "content"}}{{t "navigation.home"}}{{end}}`, packs)
	if _, err := m.InstallPackage(archive); err != nil {
		t.Fatal(err)
	}
	state, err = m.VersionState(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Activate(t.Context(), manifest.Slug, manifest.Version, state.Revision, m.Active().Slug, nil); err != nil {
		t.Fatal(err)
	}
	if got := m.Translations("en"); got.Theme != "translated-theme" {
		t.Fatalf("wrong selected theme: %s", got.Theme)
	}
	rendered.Reset()
	if err := m.Render(&rendered, "home", localeTestPage{Locale: "en"}); err != nil || rendered.String() != "Theme home" {
		t.Fatal(rendered.String(), err)
	}

	// Invalid reloads preserve the previous in-memory dictionary and templates.
	if err := os.WriteFile(filepath.Join(m.root, manifest.Slug, "locales", "en.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Load(); err == nil {
		t.Fatal("accepted invalid reload")
	}
	rendered.Reset()
	if err := m.Render(&rendered, "home", localeTestPage{Locale: "en"}); err != nil || rendered.String() != "Theme home" {
		t.Fatal("reload lost running dictionary", rendered.String(), err)
	}
}

func TestThemeInstallRejectsInvalidLanguagePack(t *testing.T) {
	t.Parallel()
	m, manifest := themeOTAFixture(t)
	manifest.Slug, manifest.DefaultLocale = "broken-theme", "en"
	for _, packs := range []map[string]string{nil, {"en": `{"schema":1,"locale":"en","messages":{"key":"first","key":"second"}}`}} {
		_, archive := translationArchive(t, manifest, `{{define "content"}}home{{end}}`, packs)
		if _, err := m.InstallPackage(archive); err == nil {
			t.Fatal("installed invalid language packs")
		}
		if _, err := os.Stat(filepath.Join(m.root, manifest.Slug)); !os.IsNotExist(err) {
			t.Fatal("failed install published files", err)
		}
	}
}

func TestThemeLocaleHelpersAreRequestLocalAndEscapeText(t *testing.T) {
	t.Parallel()
	m := newTimezoneTestManager(t, `{{t "navigation.home"}}|{{with .Name}}{{t "comments.title" (dict "count" .)}}{{end}}`)
	var workers sync.WaitGroup
	failures := make(chan error, 40)
	cases := []struct{ locale, want string }{
		{"zh-CN", "首页|评论（&lt;script&gt;）"},
		{"zh-TW", "首頁|留言（&lt;script&gt;）"},
		{"en-US", "Home|Comments (&lt;script&gt;)"},
		{"ja-JP", "ホーム|コメント（&lt;script&gt;件）"},
	}
	for n := range 40 {
		workers.Go(func() {
			test := cases[n%len(cases)]
			var output bytes.Buffer
			if err := m.Render(&output, "home", localeTestPage{Locale: test.locale, Name: "<script>"}); err != nil || output.String() != test.want {
				failures <- fmt.Errorf("%s rendered %q: %v", test.locale, output.String(), err)
			}
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	var output bytes.Buffer
	if err := m.Render(&output, "home", map[string]any{"Name": ""}); err != nil || output.String() != "首页|" {
		t.Fatal("missing provider default", output.String(), err)
	}
}

type changingLocalePage struct{ calls int }

func (p *changingLocalePage) SiteLocale() string {
	p.calls++
	if p.calls%2 == 1 {
		return "en"
	}
	return "zh-CN"
}

func TestThemeCapturesLocaleOnce(t *testing.T) {
	t.Parallel()
	m := newTimezoneTestManager(t, `{{t "navigation.home"}}|{{t "navigation.home"}}`)
	page := &changingLocalePage{}
	for _, want := range []string{"Home|Home", "首页|首页"} {
		var output bytes.Buffer
		if err := m.Render(&output, "home", page); err != nil || output.String() != want {
			t.Fatal(output.String(), err)
		}
	}
	if page.calls != 2 {
		t.Fatalf("locale read %d times", page.calls)
	}
}

func TestStarterTranslations(t *testing.T) {
	t.Parallel()
	m := NewManager(filepath.Join("..", "..", "themes"), "starter")
	if err := m.LoadExplicit("starter"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ locale, skip, latest, empty string }{
		{"zh-CN", "跳到正文", "最新文章", "暂时没有内容。"},
		{"zh-Hant", "跳至正文", "最新文章", "目前沒有內容。"},
		{"zh-TW", "跳至正文", "最新文章", "目前沒有內容。"},
		{"zh-HK", "跳至正文", "最新文章", "目前沒有內容。"},
		{"en", "Skip to content", "Latest posts", "No content yet."},
		{"en-US", "Skip to content", "Latest posts", "No content yet."},
		{"ja", "本文へ移動", "最新の記事", "まだコンテンツがありません。"},
		{"ja-JP", "本文へ移動", "最新の記事", "まだコンテンツがありません。"},
		{"de", "跳到正文", "最新文章", "暂时没有内容。"},
	} {
		for name, page := range presentation.Fixtures() {
			page.Site.Locale = test.locale
			page.Settings = m.SettingsSnapshot().Values
			for entry := range m.Active().Templates {
				page.Kind = entry
				var output RenderBuffer
				if err := m.Render(&output, entry, page); err != nil {
					t.Fatalf("%s/%s/%s: %v", test.locale, name, entry, err)
				}
				if !strings.Contains(output.String(), test.skip) || !strings.Contains(output.String(), `lang="`+test.locale+`"`) {
					t.Fatalf("%s/%s/%s has incorrect core translation or HTML lang", test.locale, name, entry)
				}
				if name == "empty" && entry == "home" && (!strings.Contains(output.String(), test.latest) || !strings.Contains(output.String(), test.empty)) {
					t.Fatalf("%s home has incorrect theme translations", test.locale)
				}
			}
		}
	}
}
