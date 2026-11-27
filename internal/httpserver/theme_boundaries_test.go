package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/coreui"
	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/presentation"
	"newpress.local/newpress/internal/theme"
)

func boundaryTheme(t *testing.T, slug, css, home string) *theme.Manager {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, slug)
	files := map[string]string{
		"theme.json":            fmt.Sprintf(`{"schema":1,"slug":%q,"name":"Independent","version":"1.0.0","engine":"go-template/v1","view":"public/v1","templates":{"home":"home.html","single":"single.html","404":"404.html","admin":"admin.html"},"settings_schema":"settings.json"}`, slug),
		"settings.json":         fmt.Sprintf(`{"type":"object","additionalProperties":false,"properties":{"accent":{"type":"string","default":"#123456","x-newpress":{"control":"color","css_variable":%q}}}}`, "--"+slug),
		"templates/base.html":   `{{define "base"}}<!doctype html><title>{{.PageTitle}}</title><link rel="stylesheet" href="{{asset "css/main.css"}}"><main>{{template "content" .}}</main>{{end}}`,
		"templates/home.html":   `{{define "content"}}` + home + `{{end}}`,
		"templates/single.html": `{{define "content"}}{{.Post.Title}}{{safeHTML .Post.ContentHTML}}{{end}}`,
		"templates/404.html":    `{{define "content"}}INDEPENDENT_404{{end}}`,
		"templates/admin.html":  `{{define "content"}}THEME_MUST_NOT_OWN_ADMIN{{end}}`,
		"assets/css/main.css":   css,
	}
	for name, value := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(value), 0640); err != nil {
			t.Fatal(err)
		}
	}
	m := theme.NewManager(root, slug, theme.WithSettingsDir(t.TempDir()))
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	return m
}

type boundaryRepository struct {
	content.Repository
	searches, terms, series int
}

func (r *boundaryRepository) SearchPublished(ctx context.Context, q content.PublishedQuery) (content.PublishedPage, error) {
	r.searches++
	return r.Repository.SearchPublished(ctx, q)
}
func (r *boundaryRepository) ListTerms(ctx context.Context, q content.TermQuery) (content.TermList, error) {
	r.terms++
	return r.Repository.ListTerms(ctx, q)
}
func (r *boundaryRepository) ListSeries(ctx context.Context, q content.SeriesQuery) (content.SeriesList, error) {
	r.series++
	return r.Repository.ListSeries(ctx, q)
}

func TestIndependentThemeCoreFlows(t *testing.T) {
	themes := boundaryTheme(t, "independent", "body{color:red}", `{{.Contract}} {{len .Posts}}`)
	users, err := auth.NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("Boundary password 2026!")
	if err != nil {
		t.Fatal(err)
	}
	authn, err := auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: users, BootstrapEmail: "owner@example.test", AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	repository := &boundaryRepository{Repository: content.NewMemoryRepository(false)}
	s := New(config.Config{SiteTitle: "Independent site", PublicURL: "https://example.test", ActiveTheme: "independent"}, repository, themes, plugin.NewManager(t.TempDir()), authn)
	h := s.routes()
	get := func(target string, cookie *http.Cookie) *httptest.ResponseRecorder {
		return serveCookieRequest(h, http.MethodGet, "https://example.test"+target, cookie, nil)
	}
	home := get("/", nil)
	if home.Code != 200 || !strings.Contains(home.Body.String(), "public/v1") {
		t.Fatalf("home: %d %s", home.Code, home.Body.String())
	}
	if repository.searches != 1 || repository.terms != 0 || repository.series != 0 {
		t.Fatalf("undeclared homepage queries: searches=%d terms=%d series=%d", repository.searches, repository.terms, repository.series)
	}
	login := serveContentForm(h, &http.Cookie{Name: "test"}, "https://example.test/admin/login/", "https://example.test", url.Values{"username": {"owner"}, "password": {"Boundary password 2026!"}, "next": {"/admin/"}})
	if login.Code != 303 || len(login.Result().Cookies()) == 0 {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	var cookie *http.Cookie
	for _, candidate := range login.Result().Cookies() {
		if candidate.Name == auth.CookieName {
			cookie = candidate
		}
	}
	if cookie == nil {
		t.Fatal("session cookie missing")
	}
	principal, ok := authn.ValidateSession(cookie.Value)
	if !ok {
		t.Fatal("login did not create session")
	}
	visited := map[string]bool{}
	for _, route := range []string{"/admin/login/", "/admin/", "/admin/content/new/", "/admin/media/", "/admin/settings/", "/admin/appearance/", "/admin/modules/", "/explore/", "/account/", "/account/access/", "/billing/"} {
		t.Run(route, func(t *testing.T) {
			session := cookie
			if route == "/admin/login/" {
				session = nil
			}
			r := get(route, session)
			if r.Code != 200 {
				t.Fatalf("core route %s: %d %s", route, r.Code, r.Body.String())
			}
			body := r.Body.String()
			if strings.Contains(body, "INDEPENDENT_404") || strings.Contains(body, "THEME_MUST_NOT_OWN_ADMIN") {
				t.Fatal("core route invoked theme fallback/admin")
			}
			if strings.HasPrefix(route, "/admin/") && strings.Contains(body, `href="/theme-settings.css`) {
				t.Fatal("admin imports active theme settings")
			}
			for _, m := range regexp.MustCompile(`(?:src|href)="([^"#]+)"`).FindAllStringSubmatch(body, -1) {
				resource := html.UnescapeString(m[1])
				if !strings.HasPrefix(resource, "/core-assets/") || visited[resource] {
					continue
				}
				visited[resource] = true
				if a := get(resource, nil); a.Code != 200 {
					t.Errorf("missing core asset %s: %d", resource, a.Code)
				}
			}
		})
	}
	form := url.Values{"csrf_token": {principal.CSRFToken}, "action": {"save"}, "type": {"post"}, "status": {"publish"}, "title": {"Independent article"}, "slug": {"independent-article"}, "content_source": {"<p>Core editor works</p>"}, "category": {"未分类"}, "comment_status": {"open"}, "published_at": {""}, "published_timezone": {"Asia/Shanghai"}, "published_source": {"canonical"}}
	created := serveContentForm(h, cookie, "https://example.test/admin/content/new/", "https://example.test", form)
	if created.Code != 303 {
		t.Fatalf("core editor create: %d %s", created.Code, created.Body.String())
	}
	if r := get("/posts/independent-article", nil); r.Code != 200 || !strings.Contains(r.Body.String(), "Core editor works") {
		t.Fatalf("public article: %d %s", r.Code, r.Body.String())
	}
	snapshot := themes.SettingsSnapshot()
	saved := serveContentForm(h, cookie, "https://example.test/admin/appearance/", "https://example.test", url.Values{"csrf_token": {principal.CSRFToken}, "action": {"save"}, "revision": {snapshot.Revision}, "accent": {"#abcdef"}})
	if saved.Code != 303 || themes.SettingsSnapshot().Values["accent"] != "#abcdef" {
		t.Fatalf("independent settings: %d %s", saved.Code, saved.Body.String())
	}
	preview := serveContentForm(h, cookie, "https://example.test/admin/appearance/preview/?page=home", "https://example.test", url.Values{"csrf_token": {principal.CSRFToken}, "accent": {"#112233"}})
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "public/v1") || themes.SettingsSnapshot().Values["accent"] != "#abcdef" {
		t.Fatalf("isolated preview: %d %s", preview.Code, preview.Body.String())
	}
	export := get("/admin/appearance/export/", cookie)
	if export.Code != 200 || !strings.Contains(export.Header().Get("Content-Disposition"), "independent-settings.json") {
		t.Fatal("theme settings export name is hardcoded")
	}
	if r := get("/missing", nil); r.Code != 404 || !strings.Contains(r.Body.String(), "INDEPENDENT_404") {
		t.Fatal("real 404 template/status mismatch")
	}
	if r := get("/admin/missing", cookie); strings.Contains(r.Body.String(), "INDEPENDENT_404") {
		t.Fatal("admin error invoked active theme")
	}
	if r := get("/admin/appearance/layout/", cookie); r.Code != 501 || !strings.Contains(r.Body.String(), "data-feature-status") {
		t.Fatal("undeclared layout must be unavailable")
	}
	beforeLayout := themes.SettingsSnapshot()
	layoutSave := serveContentForm(h, cookie, "https://example.test/admin/appearance/layout/", "https://example.test", url.Values{"csrf_token": {principal.CSRFToken}, "revision": {beforeLayout.Revision}, "accent": {"#000000"}})
	if layoutSave.Code != 501 || themes.SettingsSnapshot().Revision != beforeLayout.Revision {
		t.Fatal("undeclared layout write changed settings")
	}
	if err := themes.Load(); err != nil || themes.SettingsSnapshot().Values["accent"] != "#abcdef" {
		t.Fatal("settings were not preserved on reload", err)
	}
	icon := get("/favicon.ico", nil)
	if r := get(icon.Header().Get("Location"), nil); r.Code != 200 {
		t.Fatal("core favicon missing")
	}
	siteSnapshot := s.siteSettings.Snapshot()
	settings := siteSnapshot.Settings
	settings.Modules = map[string]bool{"community": false, "series": false, "comments": false, "feedback": false}
	if _, fields, err := s.siteSettings.Save(siteSnapshot.Revision, settings); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	for _, route := range []string{"/community/", "/series/", "/feedback/", "/api/v1/admin/comments", "/admin/comments/1/edit/"} {
		if r := get(route, cookie); r.Code != 404 {
			t.Fatalf("disabled independent route %s: %d", route, r.Code)
		}
	}
	if r := get("/admin/comments/", cookie); r.Code != 200 || !strings.Contains(r.Body.String(), "data-feature-status") || strings.Contains(r.Body.String(), "data-comment-admin") {
		t.Fatal("paused comments must retain the core diagnostic page")
	}
	siteSnapshot = s.siteSettings.Snapshot()
	settings = siteSnapshot.Settings
	settings.Operations.MaintenanceEnabled = true
	settings.Operations.MaintenanceMessage = "Independent maintenance"
	if _, fields, err := s.siteSettings.Save(siteSnapshot.Revision, settings); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	if r := get("/", nil); r.Code != 503 || !strings.Contains(r.Body.String(), settings.Operations.MaintenanceMessage) {
		t.Fatal("core maintenance fallback unavailable")
	}
	for _, route := range []string{"/admin/", coreui.AssetURL("js/newpress-client.js"), themes.AssetURL("css/main.css")} {
		if r := get(route, cookie); r.Code != 200 {
			t.Fatalf("maintenance blocked %s: %d", route, r.Code)
		}
	}
}

func TestThemeRenderFailureDoesNotCommitPartialHTML(t *testing.T) {
	for _, markup := range []string{`LEAKED_PARTIAL{{.MissingContractField}}`, strings.Repeat("x", theme.MaxRenderBytes+1)} {
		m := boundaryTheme(t, "broken", "body{}", markup)
		s := New(config.Config{ActiveTheme: "broken"}, content.NewMemoryRepository(false), m, plugin.NewManager(t.TempDir()))
		r := httptest.NewRecorder()
		s.routes().ServeHTTP(r, httptest.NewRequest("GET", "/", nil))
		if r.Code != 500 || r.Header().Get("Cache-Control") != "no-store" || strings.Contains(r.Body.String(), "LEAKED_PARTIAL") || r.Body.Len() > 4096 {
			t.Fatalf("partial response committed: status=%d bytes=%d", r.Code, r.Body.Len())
		}
	}
}

func TestThemeAssetAndCSSIdentityAcrossThemes(t *testing.T) {
	a := boundaryTheme(t, "alpha", "body{color:red}", "alpha")
	b := boundaryTheme(t, "beta", "body{color:blue}", "beta")
	if a.SettingsSnapshot().Revision == b.SettingsSnapshot().Revision {
		t.Fatal("settings revisions must bind the theme identity even with equal values")
	}
	if a.SettingsCSSSnapshot().Revision == b.SettingsCSSSnapshot().Revision || a.AssetURL("css/main.css") == b.AssetURL("css/main.css") {
		t.Fatal("cross-theme cache collision")
	}
	makeHandler := func(m *theme.Manager) http.Handler {
		return New(config.Config{ActiveTheme: m.Active().Slug}, content.NewMemoryRepository(false), m, plugin.NewManager(t.TempDir())).routes()
	}
	ha, hb := makeHandler(a), makeHandler(b)
	get := func(h http.Handler, p, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", p, nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	ca := get(ha, "/theme-settings.css", "")
	cb := get(hb, "/theme-settings.css", ca.Header().Get("ETag"))
	if cb.Code != 200 || !strings.Contains(cb.Body.String(), "--beta") {
		t.Fatalf("wrong cross-theme conditional CSS: %d", cb.Code)
	}
	if r := get(hb, a.AssetURL("css/main.css"), ""); r.Code != 404 {
		t.Fatalf("old immutable URL returned new bytes: %d", r.Code)
	}
	if r := get(ha, a.AssetURL("css/main.css"), ""); r.Code != 200 || !strings.Contains(r.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("content-identified assets should be immutable")
	}
	if r := get(ha, "/theme-assets/css/main.css?v=1.0.0", ""); r.Code != 200 || strings.Contains(r.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("legacy URL must revalidate")
	}
	if r := get(hb, "/theme-settings.css?v="+a.SettingsCSSSnapshot().Revision, ""); r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("stale CSS identity can cache new bytes")
	}
	if err := os.WriteFile(filepath.Join(a.AssetDir(), "css", "main.css"), []byte("modified without reload"), 0640); err != nil {
		t.Fatal(err)
	}
	if r := get(ha, a.AssetURL("css/main.css"), ""); r.Code != 404 {
		t.Fatal("manual edit served changed bytes under immutable URL")
	}
}

func TestBrokenErrorTemplateReturns500(t *testing.T) {
	m := boundaryTheme(t, "broken-error", "body{}", "home")
	file := filepath.Join(filepath.Dir(m.AssetDir()), "templates", "404.html")
	if err := os.WriteFile(file, []byte(`{{define "content"}}PARTIAL_ERROR{{.NotInContract}}{{end}}`), 0640); err != nil {
		t.Fatal(err)
	}
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	s := New(config.Config{ActiveTheme: "broken-error"}, content.NewMemoryRepository(false), m, plugin.NewManager(t.TempDir()))
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest("GET", "/missing", nil))
	if w.Code != 500 || strings.Contains(w.Body.String(), "PARTIAL_ERROR") {
		t.Fatal("broken error template retained successful fallback status")
	}
}

func TestPublicContractExcludesInternalState(t *testing.T) {
	p := publicPage(pageView{Post: content.Post{Title: "Public", ContentSource: "PRIVATE_SOURCE", Meta: map[string]any{"secret": "PRIVATE_META"}}, Admin: adminView{CSRFToken: "PRIVATE_ADMIN_TOKEN"}})
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PRIVATE_SOURCE", "PRIVATE_META", "PRIVATE_ADMIN_TOKEN"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public view contains %s", secret)
		}
	}
	seen := map[reflect.Type]bool{}
	var inspect func(reflect.Type)
	inspect = func(typ reflect.Type) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			inspect(typ.Elem())
		case reflect.Map:
			inspect(typ.Elem())
		case reflect.Struct:
			if typ.PkgPath() == "time" {
				return
			}
			if typ.PkgPath() != "newpress.local/newpress/internal/presentation" {
				t.Errorf("public contract imports %s", typ)
			}
			for i := 0; i < typ.NumField(); i++ {
				inspect(typ.Field(i).Type)
			}
		}
	}
	inspect(reflect.TypeFor[presentation.Page]())
	if _, exists := reflect.TypeFor[chunnelPublicView]().FieldByName("Admin"); exists {
		t.Fatal("legacy adapter exposes admin")
	}
}

func TestStarterThemeIsIndependent(t *testing.T) {
	root := t.TempDir()
	if err := extension.CopyTree(t.Context(), filepath.Join("..", "..", "themes", "starter"), filepath.Join(root, "starter")); err != nil {
		t.Fatal(err)
	}
	m := theme.NewManager(root, "starter", theme.WithSettingsDir(t.TempDir()))
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	s := New(config.Config{SiteTitle: "Starter", PublicURL: "https://example.test", ActiveTheme: "starter"}, content.NewMemoryRepository(true), m, plugin.NewManager(t.TempDir()))
	h := s.routes()
	for route, status := range map[string]int{"/": 200, "/explore/": 200, "/posts/welcome-to-newpress": 200, "/account/": 200, "/account/access/": 200, "/admin/": 200, "/not-found": 404} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "https://example.test"+route, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("starter %s: %d %s", route, w.Code, w.Body.String())
		}
	}
	if err := coreui.Validate(); err != nil {
		t.Fatal(err)
	}
	snapshot := s.siteSettings.Snapshot()
	settings := snapshot.Settings
	settings.Discussion.RequireLogin = false
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, settings); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	article := serveCommentRequest(h, nil, "GET", "https://example.test/posts/welcome-to-newpress", "", "", "198.51.100.8:4000")
	token := extractCommentToken(t, article.Body.String())
	form := url.Values{"form_token": {token}, "parent_id": {"0"}, "website": {""}, "author_name": {"独立主题读者"}, "email": {"reader@example.test"}, "content": {"独立模板提交的评论"}}
	created := servePublicCommentForm(h, "welcome-to-newpress", form, "https://example.test", "198.51.100.8:4000")
	if created.Code != 303 {
		t.Fatalf("starter comment form: %d %s", created.Code, created.Body.String())
	}
	pending, err := s.comments.ListAdmin(t.Context(), comment.Query{Status: comment.StatusPending, Limit: 10})
	if err != nil || pending.Total != 1 {
		t.Fatalf("starter comment not persisted: %v %v", pending, err)
	}
}
