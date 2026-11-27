package httpserver

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/extensionupdate"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

type themeAdminMarket struct {
	*httpExtensionMarket
	release theme.Release
	archive string
	cover   []byte
}

func (m *themeAdminMarket) InstallTheme(ctx context.Context, themes *theme.Manager, slug, digest string, p extension.Progress) error {
	if slug != m.release.Manifest.Slug || digest != m.release.SHA256 {
		return extension.ErrConflict
	}
	return themes.InstallRelease(ctx, m.archive, m.release, p)
}
func (m *themeAdminMarket) ThemeCover(ctx context.Context, slug, digest string) ([]byte, string, error) {
	if slug != m.release.Manifest.Slug || digest != m.release.Cover.SHA256 {
		return nil, "", os.ErrNotExist
	}
	mime, err := theme.ValidateCover(m.cover)
	return m.cover, mime, err
}

func themeAdminFixture(t *testing.T, external string) (*Server, http.Handler, *http.Cookie, auth.Principal, theme.Release) {
	t.Helper()
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	root := t.TempDir()
	for _, slug := range []string{"chunnel", "starter"} {
		if err := extension.CopyTree(t.Context(), filepath.Join("..", "..", "themes", slug), filepath.Join(root, slug)); err != nil {
			t.Fatal(err)
		}
	}
	s.themes = theme.NewManager(root, "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	s.config.ThemesDir = root
	source := filepath.Join(t.TempDir(), "source")
	if external == "" {
		external = filepath.Join("..", "..", "themes", "starter")
	}
	if err := extension.CopyTree(t.Context(), external, source); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(source, "theme.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest theme.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Slug == "starter" {
		manifest.Slug, manifest.Name, manifest.Description = "gallery", "Gallery", "独立主题市场测试发行版"
		manifest.Cover = "assets/preview.png"
		var cover bytes.Buffer
		if err := png.Encode(&cover, image.NewRGBA(image.Rect(0, 0, 960, 600))); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "assets/preview.png"), cover.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := extension.WriteJSON(filepath.Join(source, "theme.json"), manifest); err != nil {
		t.Fatal(err)
	}
	cover, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(manifest.Cover)))
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	if err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		file, err := writer.Create(filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "theme.zip")
	if err := os.WriteFile(archive, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	sum, coverSum := sha256.Sum256(buffer.Bytes()), sha256.Sum256(cover)
	release := theme.Release{Manifest: manifest, Path: "packages/themes/test.zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(buffer.Len()), Cover: &theme.CoverFile{Path: "previews/themes/test" + filepath.Ext(manifest.Cover), SHA256: hex.EncodeToString(coverSum[:]), Size: int64(len(cover))}, Release: &extension.Metadata{Channel: "stable", PublishedAt: time.Now().UTC(), DataCompatible: true}}
	market := &themeAdminMarket{httpExtensionMarket: &httpExtensionMarket{view: plugin.MarketView{Configured: true, Schema: 2, Expires: time.Now().Add(time.Hour), Checked: time.Now(), Themes: []theme.Release{release}, Sources: []plugin.MarketSourceStatus{{Name: "本地验收目录", Available: true, Selected: true}}}}, release: release, archive: archive, cover: cover}
	service, err := extensionupdate.New(filepath.Join(t.TempDir(), "jobs.json"), market, s.plugins, s.themes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	s.SetExtensionUpdates(service)
	return s, handler, cookie, owner, release
}

func waitThemeJob(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for s.extensions.Busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if job := s.extensions.Job("theme"); job.Phase != "succeeded" {
		t.Fatalf("theme task: %+v", job)
	}
}

func TestThemeMarketplaceLifecycleAndAuthorization(t *testing.T) {
	s, h, cookie, owner, release := themeAdminFixture(t, "")
	for _, path := range []string{"/admin/themes/", "/api/v1/admin/themes/gallery/cover?digest=" + release.Cover.SHA256} {
		w := serveCookieRequest(h, "GET", "https://example.test"+path, cookie, nil)
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
		if strings.HasPrefix(path, "/admin/") && (!strings.Contains(w.Body.String(), "data-theme-market") || strings.Contains(w.Body.String(), "/theme-assets/")) {
			t.Fatal("market is not owned by core UI")
		}
	}
	if w := serveCookieRequest(h, "GET", "https://example.test/api/v1/admin/themes/gallery/cover?digest="+release.Cover.SHA256, nil, nil); w.Code != 401 {
		t.Fatal("anonymous preview", w.Code)
	}
	request := func(action, slug string, fields map[string]any) *httptest.ResponseRecorder {
		fields["expected_user_id"], fields["channel"] = owner.UserID, "stable"
		body, _ := json.Marshal(fields)
		return serveAdminCommentAPI(h, cookie, owner.CSRFToken, "POST", "https://example.test/api/v1/admin/extensions/theme/"+slug+"/"+action, string(body), "", "https://example.test")
	}
	if w := request("install", release.Manifest.Slug, map[string]any{"version": release.Manifest.Version, "digest": release.SHA256}); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	waitThemeJob(t, s)
	if s.themes.Active().Slug != "chunnel" {
		t.Fatal("install auto-activated")
	}
	state, _ := s.themes.VersionState(release.Manifest.Slug)
	params := map[string]any{"expected_version": release.Manifest.Version, "revision": state.Revision, "expected_active": "stale"}
	if w := request("activate", release.Manifest.Slug, params); w.Code != 409 {
		t.Fatal("stale activation", w.Code, w.Body.String())
	}
	params["expected_active"] = "chunnel"
	if w := request("activate", release.Manifest.Slug, params); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	waitThemeJob(t, s)
	if s.themes.Active().Slug != release.Manifest.Slug {
		t.Fatal("theme did not activate")
	}
	if w := serveCookieRequest(h, "GET", "https://example.test/", cookie, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "/theme-assets/gallery/") {
		t.Fatal("new public theme not used", w.Code)
	}
	params["expected_active"] = release.Manifest.Slug
	if w := request("uninstall", release.Manifest.Slug, params); w.Code != 422 {
		t.Fatal("active uninstall", w.Code)
	}
	first := s.themes.Active()
	starterState, _ := s.themes.VersionState("starter")
	items, _ := s.themes.List()
	var starterVersion string
	for _, item := range items {
		if item.Slug == "starter" {
			starterVersion = item.Version
		}
	}
	if w := request("activate", "starter", map[string]any{"expected_version": starterVersion, "revision": starterState.Revision, "expected_active": first.Slug}); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	waitThemeJob(t, s)
	params["expected_active"] = "starter"
	if w := request("uninstall", release.Manifest.Slug, params); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	waitThemeJob(t, s)
	if _, err := s.themes.VersionState(release.Manifest.Slug); err == nil {
		t.Fatal("uninstall left installation")
	}
	if w := serveCookieRequest(h, "GET", "https://example.test/api/v1/admin/themes/gallery/cover", cookie, nil); w.Code != 404 {
		t.Fatal("uninstalled local cover still served", w.Code)
	}
}

func TestThemeMarketplaceAutomaticCheckFreshness(t *testing.T) {
	s, handler, cookie, _, _ := themeAdminFixture(t, "")
	market := &httpExtensionMarket{view: plugin.MarketView{Configured: true, Schema: 2, Expires: time.Now().Add(time.Hour)}}
	service, err := extensionupdate.New(filepath.Join(t.TempDir(), "auto.json"), market, s.plugins, s.themes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	s.SetExtensionUpdates(service)
	assertAuto := func(want bool) {
		t.Helper()
		w := serveCookieRequest(handler, "GET", "https://example.test/admin/themes/", cookie, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), `data-auto-check="true"`) != want {
			t.Fatalf("market automatic check want=%v status=%d", want, w.Code)
		}
	}
	assertAuto(true)
	market.view.Checked = time.Now()
	assertAuto(false)
	market.view.Checked = time.Now().Add(-time.Minute)
	assertAuto(true)
	market.view.Configured = false
	assertAuto(false)
	market.view.Configured = true
	market.view.Checked = time.Time{}
	// A source failure must not trigger an automatic check/reload loop even
	// when no catalog timestamp was persisted by that failed operation.
	market.check = func(context.Context) error { return context.DeadlineExceeded }
	if _, err := service.Start(extensionupdate.Request{Kind: "theme", Action: "check", Channel: "stable"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for service.Busy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if service.Job("theme").Phase != "failed" {
		t.Fatal("expected a failed source check")
	}
	assertAuto(false)
}

// This opt-in fixture installs only into temporary directories, including when
// NEWPRESS_THEME_MARKET_SOURCE points at a separate theme development checkout.
func TestThemeMarketplaceBrowser(t *testing.T) {
	artifacts := os.Getenv("NEWPRESS_THEME_MARKET_UI")
	if artifacts == "" {
		t.Skip("opt-in browser fixture")
	}
	if err := os.MkdirAll(artifacts, 0750); err != nil {
		t.Fatal(err)
	}
	s, handler, cookie, _, _ := themeAdminFixture(t, os.Getenv("NEWPRESS_THEME_MARKET_SOURCE"))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/themes/", http.StatusSeeOther)
	})
	mux.Handle("/", handler)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	if err := extension.WriteJSON(filepath.Join(artifacts, "fixture.json"), map[string]string{"url": host.URL + "/fixture/login"}); err != nil {
		t.Fatal(err)
	}
	t.Log("theme market browser fixture", host.URL)
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("browser fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(artifacts, "stop")); err == nil {
				return
			}
		}
	}
}
