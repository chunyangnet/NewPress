package theme

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testPage struct {
	CodeLoginEnabled bool
	PageTitle        string
	Description      string
	Canonical        string
	BodyClass        string
	Kind             string
	ActiveNav        string
	SearchQuery      string
	Category         string
	Home             map[string]any
	Images           map[string]any
	Integrations     struct {
		ICPNumber, PoliceNumber, PoliceURL string
		FooterCopyright, FooterHTML        string
		FooterHideCredit                   bool
		Verification                       []struct{ Name, Content string }
		AnalyticsConfig, ScriptURL         string
	}
	SEO struct {
		Kind, Published, Modified, ImageAlt, JSONLD string
		ImageWidth, ImageHeight                     int
	}
	Site struct {
		Title              string
		Tagline            string
		Description        string
		PublicURL          string
		ActiveTheme        string
		ThemeVersion       string
		Locale             string
		Timezone           string
		IconURL            string
		SocialImageURL     string
		Generator          string
		Robots             string
		PublicAPIEnabled   bool
		MaintenanceMessage string
	}
	Posts               []any
	Featured            []any
	Categories          []any
	PluginFragments     []any
	LoginProviders      []any
	Version             string
	GoVersion           string
	Now                 time.Time
	IsAdmin             bool
	IsPreview           bool
	VerificationEnabled bool
	Admin               struct{ Current string }
	ThemeValues         map[string]any
	ThemeClass          string
	ThemeCSSURL         string
	Navigation          struct {
		CSSURL                 string
		HasBadges              bool
		PrimaryConfigured      bool
		UtilityConfigured      bool
		MobileConfigured       bool
		MobileActionConfigured bool
		FooterConfigured       bool
		Primary                []any
		Utility                []any
		Mobile                 []any
		MobileDock             []any
		MobileAction           []any
		Footer                 []any
	}
}

func (testPage) ModuleEnabled(string) bool { return true }

func TestChunnelLoadsAndRenders(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "themes")
	manager := NewManager(root, "chunnel")
	if err := manager.Load(); err != nil {
		t.Fatalf("load chunnel: %v", err)
	}
	page := testPage{PageTitle: "Test", Description: "Description", Now: time.Now(), ThemeValues: map[string]any{
		"floating_tools": true, "floating_top": true, "floating_notifications": true, "floating_search": true, "mobile_nav_enabled": true, "mobile_quick_action": true, "community_nav": true, "market_nav": true, "utility_nav": true, "search_enabled": true,
		"hero_enabled": true, "hero_label": "主题设计", "hero_title": "让好内容穿过信息噪声", "hero_summary": "Description",
		"quick_links_enabled": true, "collection_enabled": true, "community_module": true, "sidebar_enabled": true, "market_module": true,
		"lazy_images": true, "reading_progress": true, "article_author_box": true, "download_module": true, "comments_module": true,
		"footer_note": "Test",
	}}
	defaults := manager.SettingsSnapshot().Values
	maps.Copy(defaults, page.ThemeValues)
	page.ThemeValues = defaults
	page.BodyClass = "is-home"
	page.Kind = "home"
	page.Home = map[string]any{
		"DateLabel": "11月12日", "Weekday": "星期四",
		"Top": []any{map[string]any{"Key": "hero", "Title": "封面精选", "Visibility": "both"}},
	}
	page.Images = map[string]any{}
	page.Site.Title = "NewPress"
	page.Site.Tagline = "Test site"
	page.Site.ActiveTheme = "chunnel"
	page.Site.ThemeVersion = "0.7.1"
	page.Site.Locale = "zh-CN"
	page.Site.Timezone = "Asia/Shanghai"
	page.Site.IconURL = "/favicon.svg"
	page.Site.PublicAPIEnabled = true
	page.Navigation.CSSURL = "/navigation.css?v=test"
	page.Navigation.MobileDock = []any{map[string]any{"ID": 1, "URL": "/", "Label": "首页", "Icon": "house", "Active": true}}
	var output bytes.Buffer
	if err := manager.Render(&output, "home", page); err != nil {
		t.Fatalf("render chunnel: %v", err)
	}
	if body := output.String(); !strings.Contains(body, "让好内容") || !strings.Contains(body, "chunnel-home.css") {
		t.Fatalf("unexpected rendered output: %.200s", body)
	}
	body := output.String()
	for _, marker := range []string{`id="icon-search"`, `id="icon-arrow-up-to-line"`, `id="icon-bell-ring"`, `href="#icon-bookmark"`, `class="icon icon-house"`} {
		if !strings.Contains(body, marker) {
			t.Fatalf("rendered theme is missing shared SVG icon marker %q", marker)
		}
	}
	for _, legacyGlyph := range []string{"⌕", "♡", "▤", "♙", "◇"} {
		if strings.Contains(body, legacyGlyph) {
			t.Fatalf("rendered theme still contains legacy icon glyph %q", legacyGlyph)
		}
	}
}

func TestManagerRejectsActiveThemeOutsideSlugBoundary(t *testing.T) {
	t.Parallel()
	// A persisted selection legitimately takes precedence over the fallback.
	// Exercise fallback validation without reading the developer's local choice.
	root := t.TempDir()
	for _, active := range []string{"../chunnel", ".", "C:\\themes\\chunnel", "x"} {
		if err := NewManager(root, active).Load(); err == nil || !strings.Contains(err.Error(), "invalid package slug") {
			t.Fatalf("unsafe active theme %q: expected slug validation error, got %v", active, err)
		}
	}
}

func TestManagerRejectsManifestSlugMismatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "safe-theme")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":1,"slug":"other-theme","name":"Other","version":"1.0.0","engine":"go-template/v1","view":"public/v1","templates":{"home":"home.html","single":"single.html"}}`
	if err := os.WriteFile(filepath.Join(directory, "theme.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewManager(root, "safe-theme").Load(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("manifest slug mismatch error = %v", err)
	}
}

func TestManagerRejectsSymlinkThemeFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "linked-theme")
	if err := os.MkdirAll(filepath.Join(directory, "templates"), 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Schema: 1, Slug: "linked-theme", Name: "Linked", Version: "1.0.0", Engine: "go-template/v1", View: "public/v1",
		Templates: map[string]string{"home": "home.html", "single": "single.html"},
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "theme.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	base := `{{define "base"}}{{template "content" .}}{{end}}`
	if err := os.WriteFile(filepath.Join(directory, "templates", "base.html"), []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.html")
	if err := os.WriteFile(outside, []byte(`{{define "content"}}outside{{end}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "templates", "home.html")); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "templates", "single.html"), []byte(`{{define "content"}}single{{end}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewManager(root, "linked-theme").Load(); err == nil {
		t.Fatal("loaded theme with symlink template")
	}
}

func TestManagerRejectsSymlinkThemePartials(t *testing.T) {
	t.Parallel()
	writeTheme := func(t *testing.T, root string) string {
		t.Helper()
		directory := filepath.Join(root, "linked-partials")
		if err := os.MkdirAll(filepath.Join(directory, "templates"), 0o750); err != nil {
			t.Fatal(err)
		}
		manifest := Manifest{
			Schema: 1, Slug: "linked-partials", Name: "Linked partials", Version: "1.0.0", Engine: "go-template/v1", View: "public/v1",
			Templates: map[string]string{"home": "home.html", "single": "single.html"},
		}
		payload, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{
			"theme.json":            string(payload),
			"templates/base.html":   `{{define "base"}}{{template "content" .}}{{end}}`,
			"templates/home.html":   `{{define "content"}}home{{end}}`,
			"templates/single.html": `{{define "content"}}single{{end}}`,
		} {
			if err := os.WriteFile(filepath.Join(directory, filepath.FromSlash(name)), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return directory
	}

	t.Run("file", func(t *testing.T) {
		root := t.TempDir()
		directory := writeTheme(t, root)
		partials := filepath.Join(directory, "templates", "partials")
		if err := os.MkdirAll(partials, 0o750); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.html")
		if err := os.WriteFile(outside, []byte(`{{define "partial"}}outside{{end}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(partials, "outside.html")); err != nil {
			t.Skipf("creating symlink is unavailable: %v", err)
		}
		if err := NewManager(root, "linked-partials").Load(); err == nil {
			t.Fatal("loaded theme with linked partial")
		}
	})

	t.Run("directory", func(t *testing.T) {
		root := t.TempDir()
		directory := writeTheme(t, root)
		outside := filepath.Join(t.TempDir(), "partials")
		if err := os.MkdirAll(outside, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "outside.html"), []byte(`{{define "partial"}}outside{{end}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(directory, "templates", "partials")); err != nil {
			t.Skipf("creating symlink is unavailable: %v", err)
		}
		if err := NewManager(root, "linked-partials").Load(); err == nil {
			t.Fatal("loaded theme through linked partials directory")
		}
	})
}

func TestValidateThemeManifestRejectsNonCanonicalPathsAndLargeCollections(t *testing.T) {
	t.Parallel()
	manifest := Manifest{
		Schema: 1, Slug: "safe-theme", Name: "Safe", Version: "1.0.0", Engine: "go-template/v1", View: "public/v1",
		Templates: map[string]string{"home": "home.html", "single": "single.html"},
	}
	for name, mutate := range map[string]func(*Manifest){
		"backslash template": func(value *Manifest) { value.Templates["home"] = `dir\\home.html` },
		"dot template":       func(value *Manifest) { value.Templates["home"] = "./home.html" },
		"unsafe settings":    func(value *Manifest) { value.SettingsSchema = "../settings.json" },
		"too many templates": func(value *Manifest) {
			for index := 0; index < 129; index++ {
				value.Templates[fmt.Sprintf("page-%03d", index)] = fmt.Sprintf("page-%03d.html", index)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := manifest
			candidate.Templates = maps.Clone(manifest.Templates)
			mutate(&candidate)
			if err := validateManifest(candidate); err == nil {
				t.Fatal("accepted unsafe theme manifest")
			}
		})
	}
}

func TestInstallInvalidThemeLeavesNoInstallArtifacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "invalid-theme.zip")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveWriter := zip.NewWriter(archiveFile)
	manifest := Manifest{
		Schema: 1, Slug: "broken-theme", Name: "Broken", Version: "1.0.0", Engine: "go-template/v1", View: "public/v1",
		Templates: map[string]string{"home": "home.html", "single": "single.html"},
	}
	manifestEntry, err := archiveWriter.Create("theme.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(manifestEntry).Encode(manifest); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"templates/base.html":   "{{",
		"templates/home.html":   "{{define \"base\"}}{{end}}",
		"templates/single.html": "{{define \"base\"}}{{end}}",
	} {
		entry, err := archiveWriter.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archiveWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := NewManager(root, "broken-theme").InstallPackage(archivePath); err == nil {
		t.Fatal("accepted theme with invalid template syntax")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed theme installation left artifacts: %v", entries)
	}
}

func TestThemeListRejectsLinkedRoot(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	root := filepath.Join(t.TempDir(), "themes")
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if _, err := NewManager(root, "linked-theme").List(); err == nil || !strings.Contains(err.Error(), "theme root") {
		t.Fatalf("linked theme root listing error = %v", err)
	}
}

func TestThemeLoadRejectsLinkedManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "linked-manifest")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Schema: 1, Slug: "linked-manifest", Name: "Linked manifest", Version: "1.0.0", Engine: "go-template/v1", View: "public/v1",
		Templates: map[string]string{"home": "home.html", "single": "single.html"},
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "theme.json")
	if err := os.WriteFile(outside, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "theme.json")); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if err := NewManager(root, manifest.Slug).Load(); err == nil {
		t.Fatal("loaded theme through linked manifest")
	}
}

func TestInstallRejectsLinkedThemeTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manifest := Manifest{
		Schema: 1, Slug: "linked-target", Name: "Linked target", Version: "1.0.0", Engine: "go-template/v1", View: "public/v1",
		Templates: map[string]string{"home": "home.html", "single": "single.html"},
	}
	archivePath := createThemeArchive(t, manifest)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, manifest.Slug)); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if _, err := NewManager(root, manifest.Slug).InstallPackage(archivePath); err == nil {
		t.Fatal("installed theme through linked target")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != manifest.Slug {
		t.Fatalf("linked target installation artifacts = %v", entries)
	}
}

func createThemeArchive(t *testing.T, manifest Manifest) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "theme.zip")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveWriter := zip.NewWriter(archiveFile)
	writeEntry := func(name, body string) {
		entry, err := archiveWriter.Create(name)
		if err != nil {
			archiveWriter.Close()
			archiveFile.Close()
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			archiveWriter.Close()
			archiveFile.Close()
			t.Fatal(err)
		}
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		archiveWriter.Close()
		archiveFile.Close()
		t.Fatal(err)
	}
	writeEntry("theme.json", string(manifestPayload))
	writeEntry("templates/base.html", `{{define "base"}}{{template "content" .}}{{end}}`)
	writeEntry("templates/home.html", `{{define "content"}}home{{end}}`)
	writeEntry("templates/single.html", `{{define "content"}}single{{end}}`)
	if err := archiveWriter.Close(); err != nil {
		archiveFile.Close()
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}
