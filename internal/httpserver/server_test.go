package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/coreui"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func TestHomeAndSecurityHeaders(t *testing.T) {
	t.Parallel()
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel")
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	plugins := plugin.NewManager(t.TempDir())
	cfg := config.Config{
		SiteTitle: "NewPress", SiteTagline: "Test", PublicURL: "https://example.test",
		ActiveTheme: "chunnel",
	}
	server := New(cfg, content.NewMemoryRepository(true), themes, plugins)
	request := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "NewPress") {
		t.Fatal("home page did not render")
	}
	if policy := response.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "object-src 'none'") {
		t.Fatalf("unexpected CSP: %q", policy)
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff header")
	}
}

func TestThemeStyleFollowsAdministratorSettings(t *testing.T) {
	t.Parallel()
	server, handler, _, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	for _, style := range []string{"classic", "liquid-glass"} {
		form := url.Values{"action": {"save"}, "csrf_token": {owner.CSRFToken}, "revision": {server.themes.SettingsSnapshot().Revision}}
		for key, value := range server.themes.SettingsSnapshot().Values {
			form.Set(key, fmt.Sprint(value))
		}
		form.Set("visual_style", style)
		saved := serveContentForm(handler, ownerCookie, "https://example.test/admin/appearance/", "https://example.test", form)
		if saved.Code != http.StatusSeeOther || server.themes.SettingsSnapshot().Values["visual_style"] != style {
			t.Fatalf("administrator could not save %s: HTTP %d", style, saved.Code)
		}
		color := fmt.Sprint(server.themes.SettingsSnapshot().Values["paper_color"])
		for _, target := range []string{"/", "/explore/", "/admin/login/", "/admin/", "/admin/appearance/"} {
			request := httptest.NewRequest(http.MethodGet, "https://example.test"+target, nil)
			if target == "/admin/" || target == "/admin/appearance/" {
				request.AddCookie(ownerCookie)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			expectedStyle := style
			if strings.HasPrefix(target, "/admin/") {
				expectedStyle = "classic"
			}
			for _, marker := range []string{`data-style="` + expectedStyle + `"`, `name="theme-color" content="` + color + `"`} {
				if response.Code != http.StatusOK || !strings.Contains(body, marker) {
					t.Fatalf("%s rendered %s without %q (HTTP %d)", target, style, marker, response.Code)
				}
			}
			glassAsset := "chunnel-glass.css"
			if target == "/" {
				glassAsset = "chunnel-home-glass.css"
			}
			if got, want := strings.Contains(body, glassAsset), (style == "liquid-glass" && !strings.HasPrefix(target, "/admin/")) || target == "/admin/appearance/"; got != want {
				t.Fatalf("%s glass stylesheet = %t, want %t for %s", target, got, want, style)
			}
			for _, removed := range []string{"chunnel-style.js", "data-style-toggle", "data-style-default", "style-switch", "chunnel-visual-style"} {
				if strings.Contains(body, removed) {
					t.Fatalf("%s still exposes browser style switching: %s", target, removed)
				}
			}
		}
	}
}

func TestRequestIDAcceptsOnlyOneSafeValue(t *testing.T) {
	handler := requestID(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Seen-Request-ID", request.Header.Get("X-Request-ID"))
		writer.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		name   string
		values []string
		keep   bool
	}{
		{name: "safe", values: []string{"edge.req_2026:08-21"}, keep: true},
		{name: "space", values: []string{"edge request"}},
		{name: "unicode", values: []string{"请求-2026"}},
		{name: "comma", values: []string{"first,second"}},
		{name: "duplicate", values: []string{"first", "second"}},
		{name: "too long", values: []string{strings.Repeat("a", 129)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
			for _, value := range test.values {
				request.Header.Add("X-Request-ID", value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			got := response.Header().Get("X-Request-ID")
			if got == "" || got != response.Header().Get("Seen-Request-ID") {
				t.Fatalf("request ID propagation = response:%q seen:%q", got, response.Header().Get("Seen-Request-ID"))
			}
			if test.keep && got != test.values[0] {
				t.Fatalf("safe request ID = %q", got)
			}
			if !test.keep {
				for _, value := range test.values {
					if got == value {
						t.Fatalf("unsafe request ID was preserved: %q", got)
					}
				}
				if len(got) != 24 {
					t.Fatalf("generated request ID = %q", got)
				}
			}
		})
	}
}

func TestPublicUIAssets(t *testing.T) {
	t.Parallel()
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel")
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	server := New(
		config.Config{SiteTitle: "NewPress", ActiveTheme: "chunnel"},
		content.NewMemoryRepository(true),
		themes,
		plugin.NewManager(t.TempDir()),
	)
	handler := server.routes()

	faviconRequest := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	faviconResponse := httptest.NewRecorder()
	handler.ServeHTTP(faviconResponse, faviconRequest)
	if faviconResponse.Code != http.StatusTemporaryRedirect || faviconResponse.Header().Get("Location") != coreui.AssetURL("images/favicon.svg") {
		t.Fatalf("favicon redirect = %d %q", faviconResponse.Code, faviconResponse.Header().Get("Location"))
	}
	if faviconResponse.Header().Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("favicon cache = %q", faviconResponse.Header().Get("Cache-Control"))
	}
	faviconAssetRequest := httptest.NewRequest(http.MethodGet, faviconResponse.Header().Get("Location"), nil)
	faviconAssetResponse := httptest.NewRecorder()
	handler.ServeHTTP(faviconAssetResponse, faviconAssetRequest)
	if faviconAssetResponse.Code != http.StatusOK || !strings.Contains(faviconAssetResponse.Header().Get("Content-Type"), "image/svg+xml") || faviconAssetResponse.Body.Len() == 0 {
		t.Fatalf("favicon asset = %d type=%q bytes=%d", faviconAssetResponse.Code, faviconAssetResponse.Header().Get("Content-Type"), faviconAssetResponse.Body.Len())
	}

	assetRequest := httptest.NewRequest(http.MethodGet, "/theme-assets/css/chunnel-ui.css", nil)
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK {
		t.Fatalf("UI asset status = %d", assetResponse.Code)
	}
	if contentType := assetResponse.Header().Get("Content-Type"); !strings.Contains(contentType, "text/css") {
		t.Fatalf("UI asset content type = %q", contentType)
	}
	if body := assetResponse.Body.String(); !strings.Contains(body, "Inter") || !strings.Contains(body, "../fonts/barlow-semi-condensed-medium.woff2") {
		t.Fatal("UI asset did not reference the bundled font families")
	}

	fontRequest := httptest.NewRequest(http.MethodGet, "/theme-assets/fonts/barlow-semi-condensed-medium.woff2", nil)
	fontResponse := httptest.NewRecorder()
	handler.ServeHTTP(fontResponse, fontRequest)
	if fontResponse.Code != http.StatusOK {
		t.Fatalf("font asset status = %d", fontResponse.Code)
	}
	if contentType := fontResponse.Header().Get("Content-Type"); !strings.Contains(contentType, "font/woff2") {
		t.Fatalf("font asset content type = %q", contentType)
	}
	if fontResponse.Body.Len() == 0 {
		t.Fatal("font asset was empty")
	}
}

func TestExpandedChunnelPagesAndSearch(t *testing.T) {
	t.Parallel()
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel")
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	server := New(
		config.Config{SiteTitle: "NewPress", SiteTagline: "Test", PublicURL: "https://example.test", ActiveTheme: "chunnel"},
		content.NewMemoryRepository(true),
		themes,
		plugin.NewManager(t.TempDir()),
	)
	handler := server.routes()

	for _, test := range []struct {
		path   string
		marker string
	}{
		{path: "/explore/", marker: "发现内容"},
		{path: "/community/", marker: "社区讨论"},
		{path: "/authors/newpress-team/", marker: "关于 NewPress Team"},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", test.path, response.Code)
		}
		body := response.Body.String()
		if !strings.Contains(body, test.marker) || !strings.Contains(body, "chunnel-pages.css") {
			t.Fatalf("%s did not render expanded Chunnel page", test.path)
		}
	}

	searchRequest := httptest.NewRequest(http.MethodGet, "/explore/?q=WASM", nil)
	searchResponse := httptest.NewRecorder()
	handler.ServeHTTP(searchResponse, searchRequest)
	if searchResponse.Code != http.StatusOK || !strings.Contains(searchResponse.Body.String(), "插件不再拥有整个服务器") {
		t.Fatal("explore search did not filter matching content")
	}
	// Recommendations are independent of the search. Check the result area,
	// not the separate featured cards and recent-content sidebar.
	_, resultArea, found := strings.Cut(searchResponse.Body.String(), "data-server-results>")
	if !found {
		t.Fatal("explore did not render a server-owned result area")
	}
	resultArea, _, _ = strings.Cut(resultArea, `class="portal-sidebar"`)
	if strings.Contains(resultArea, "Chunnel（隧）：一套主题，适配六种站点") {
		t.Fatal("explore search returned a non-matching article")
	}

	assetRequest := httptest.NewRequest(http.MethodGet, "/theme-assets/css/chunnel-pages.css", nil)
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK || !strings.Contains(assetResponse.Body.String(), ".community-hero") {
		t.Fatal("expanded page stylesheet was not served")
	}
}

func TestPostsAPILimitValidation(t *testing.T) {
	t.Parallel()
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel")
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	server := New(config.Config{ActiveTheme: "chunnel"}, content.NewMemoryRepository(true), themes, plugin.NewManager(t.TempDir()))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/posts?limit=1000", nil)
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestSafeAssetPathRejectsTraversal(t *testing.T) {
	t.Parallel()
	for _, candidate := range []string{
		"/theme-assets/../theme.json",
		"/theme-assets/%2e%2e/theme.json",
		"/theme-assets/css\\secret.css",
		"/theme-assets/C:/secret.txt",
	} {
		if _, err := safeAssetPath(candidate); err == nil {
			t.Fatalf("expected %q to be rejected", candidate)
		}
	}
	if value, err := safeAssetPath("/theme-assets/css/chunnel.css"); err != nil || value != "css/chunnel.css" {
		t.Fatalf("valid asset = %q, %v", value, err)
	}
}

func TestThemeAssetsRejectSymlinkFiles(t *testing.T) {
	t.Parallel()
	themeRoot := t.TempDir()
	directory := filepath.Join(themeRoot, "linked-theme")
	assets := filepath.Join(directory, "assets")
	if err := os.MkdirAll(assets, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(themeRoot, "outside.css")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assets, "linked.css")); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	themes := theme.NewManager(themeRoot, "linked-theme")
	server := &Server{themes: themes}
	request := httptest.NewRequest(http.MethodGet, "/theme-assets/linked.css", nil)
	response := httptest.NewRecorder()
	server.themeAssets().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("symlink asset status = %d, want 404", response.Code)
	}
}

func TestThemeAssetsRejectSymlinkDirectories(t *testing.T) {
	t.Parallel()
	themeRoot := t.TempDir()
	directory := filepath.Join(themeRoot, "linked-theme")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	outsideAssets := filepath.Join(t.TempDir(), "assets")
	if err := os.MkdirAll(outsideAssets, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideAssets, "linked.css"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideAssets, filepath.Join(directory, "assets")); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	themes := theme.NewManager(themeRoot, "linked-theme")
	server := &Server{themes: themes}
	request := httptest.NewRequest(http.MethodGet, "/theme-assets/linked.css", nil)
	response := httptest.NewRecorder()
	server.themeAssets().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("symlink asset directory status = %d, want 404", response.Code)
	}
}

func TestAdminAuthenticationCookieCSRFAndProtectedAPI(t *testing.T) {
	password := "correct horse battery staple 2026"
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	adminAuth, err := auth.New(auth.Config{
		Username: "editor", PasswordHash: passwordHash, AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	server := New(
		config.Config{
			SiteTitle: "NewPress", PublicURL: "https://example.test", ActiveTheme: "chunnel",
			AdminSessionTTL: time.Hour,
		},
		content.NewMemoryRepository(true), themes, plugin.NewManager(t.TempDir()), adminAuth,
	)
	handler := server.routes()

	adminRequest := httptest.NewRequest(http.MethodGet, "https://example.test/admin/", nil)
	adminResponse := httptest.NewRecorder()
	handler.ServeHTTP(adminResponse, adminRequest)
	if adminResponse.Code != http.StatusSeeOther || !strings.HasPrefix(adminResponse.Header().Get("Location"), "/admin/login/") {
		t.Fatalf("unauthenticated admin response = %d %q", adminResponse.Code, adminResponse.Header().Get("Location"))
	}

	apiRequest := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/system", nil)
	apiResponse := httptest.NewRecorder()
	handler.ServeHTTP(apiResponse, apiRequest)
	if apiResponse.Code != http.StatusUnauthorized || apiResponse.Header().Get("WWW-Authenticate") == "" ||
		!strings.Contains(apiResponse.Body.String(), `"code":"auth.authentication_required"`) ||
		!strings.Contains(apiResponse.Body.String(), `"request_id":`) {
		t.Fatalf("unauthenticated API response = %d, auth=%q, body=%s", apiResponse.Code, apiResponse.Header().Get("WWW-Authenticate"), apiResponse.Body.String())
	}

	crossSiteForm := url.Values{"username": {"editor"}, "password": {password}}
	crossSiteRequest := httptest.NewRequest(http.MethodPost, "https://example.test/admin/login/", strings.NewReader(crossSiteForm.Encode()))
	crossSiteRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	crossSiteRequest.Header.Set("Origin", "https://attacker.test")
	crossSiteRequest.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSiteResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossSiteResponse, crossSiteRequest)
	if crossSiteResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-site login status = %d", crossSiteResponse.Code)
	}
	crossSchemeRequest := httptest.NewRequest(http.MethodPost, "https://example.test/admin/login/", strings.NewReader(crossSiteForm.Encode()))
	crossSchemeRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	crossSchemeRequest.Header.Set("Origin", "http://example.test")
	crossSchemeRequest.Header.Set("Sec-Fetch-Site", "same-site")
	crossSchemeResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossSchemeResponse, crossSchemeRequest)
	if crossSchemeResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-scheme login status = %d", crossSchemeResponse.Code)
	}

	loginForm := url.Values{
		"username": {"editor"}, "password": {password}, "next": {"https://attacker.test/steal"},
	}
	loginRequest := httptest.NewRequest(http.MethodPost, "https://example.test/admin/login/", strings.NewReader(loginForm.Encode()))
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRequest.Header.Set("Origin", "https://example.test")
	loginRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusSeeOther || loginResponse.Header().Get("Location") != "/admin/" {
		t.Fatalf("login response = %d %q", loginResponse.Code, loginResponse.Header().Get("Location"))
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %#v", cookies)
	}
	sessionCookie := cookies[0]
	if sessionCookie.MaxAge <= 0 {
		t.Fatal("session cookie expired")
	}
	if sessionCookie.Name != auth.CookieName || sessionCookie.Value == "" || sessionCookie.Path != "/" || !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("insecure admin cookie: %#v", sessionCookie)
	}

	authenticatedAPI := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/system", nil)
	authenticatedAPI.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "stale-legacy-session-token-that-is-invalid"})
	authenticatedAPI.AddCookie(sessionCookie)
	authenticatedAPIResponse := httptest.NewRecorder()
	handler.ServeHTTP(authenticatedAPIResponse, authenticatedAPI)
	if authenticatedAPIResponse.Code != http.StatusOK || !strings.Contains(authenticatedAPIResponse.Body.String(), `"active_theme"`) {
		t.Fatalf("authenticated API response = %d %s", authenticatedAPIResponse.Code, authenticatedAPIResponse.Body.String())
	}

	appearanceRequest := httptest.NewRequest(http.MethodGet, "https://example.test/admin/appearance/", nil)
	appearanceRequest.AddCookie(sessionCookie)
	appearanceResponse := httptest.NewRecorder()
	handler.ServeHTTP(appearanceResponse, appearanceRequest)
	if appearanceResponse.Code != http.StatusOK {
		t.Fatalf("appearance status = %d", appearanceResponse.Code)
	}
	match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(appearanceResponse.Body.String())
	if len(match) != 2 {
		t.Fatal("authenticated appearance page did not contain a CSRF token")
	}
	csrfToken := match[1]

	badCSRFForm := url.Values{"action": {"reset"}, "csrf_token": {"invalid"}}
	badCSRFRequest := httptest.NewRequest(http.MethodPost, "https://example.test/admin/appearance/", strings.NewReader(badCSRFForm.Encode()))
	badCSRFRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badCSRFRequest.Header.Set("Origin", "https://example.test")
	badCSRFRequest.AddCookie(sessionCookie)
	badCSRFResponse := httptest.NewRecorder()
	handler.ServeHTTP(badCSRFResponse, badCSRFRequest)
	if badCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("invalid CSRF status = %d", badCSRFResponse.Code)
	}

	logoutForm := url.Values{"csrf_token": {csrfToken}}
	logoutRequest := httptest.NewRequest(http.MethodPost, "https://example.test/admin/logout/", strings.NewReader(logoutForm.Encode()))
	logoutRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	logoutRequest.Header.Set("Origin", "https://example.test")
	logoutRequest.AddCookie(sessionCookie)
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusSeeOther || logoutResponse.Header().Get("Location") != "/admin/login/" {
		t.Fatalf("logout response = %d %q", logoutResponse.Code, logoutResponse.Header().Get("Location"))
	}
	cleared := logoutResponse.Result().Cookies()
	if len(cleared) != 1 {
		t.Fatalf("logout cookies = %#v", cleared)
	}
	clearedPaths := map[string]bool{}
	for _, cookie := range cleared {
		if cookie.MaxAge >= 0 {
			t.Fatalf("logout emitted a non-expiring cookie: %#v", cookie)
		}
		clearedPaths[cookie.Path] = true
	}
	if !clearedPaths["/"] {
		t.Fatalf("logout did not clear the session cookie: %#v", cleared)
	}

	revokedAPI := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/system", nil)
	revokedAPI.AddCookie(sessionCookie)
	revokedAPIResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokedAPIResponse, revokedAPI)
	if revokedAPIResponse.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session API status = %d", revokedAPIResponse.Code)
	}
}

func TestUnconfiguredAdminIsLoopbackReadOnlyAndHiddenExternally(t *testing.T) {
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	server := New(
		config.Config{SiteTitle: "NewPress", PublicURL: "http://localhost:8080", ActiveTheme: "chunnel"},
		content.NewMemoryRepository(true), themes, plugin.NewManager(t.TempDir()),
	)
	handler := server.routes()

	loopbackAdmin := httptest.NewRequest(http.MethodGet, "http://localhost/admin/", nil)
	loopbackAdmin.RemoteAddr = "127.0.0.1:41234"
	loopbackAdminResponse := httptest.NewRecorder()
	handler.ServeHTTP(loopbackAdminResponse, loopbackAdmin)
	if loopbackAdminResponse.Code != http.StatusOK || !strings.Contains(loopbackAdminResponse.Body.String(), "本机只读预览") {
		t.Fatalf("loopback read-only admin = %d", loopbackAdminResponse.Code)
	}

	externalAdmin := httptest.NewRequest(http.MethodGet, "http://example.test/admin/", nil)
	externalAdmin.RemoteAddr = "198.51.100.20:41234"
	externalAdminResponse := httptest.NewRecorder()
	handler.ServeHTTP(externalAdminResponse, externalAdmin)
	if externalAdminResponse.Code != http.StatusNotFound {
		t.Fatalf("external unconfigured admin status = %d", externalAdminResponse.Code)
	}

	loopbackWrite := httptest.NewRequest(http.MethodPost, "http://localhost/admin/appearance/", strings.NewReader("action=reset"))
	loopbackWrite.RemoteAddr = "[::1]:41234"
	loopbackWrite.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loopbackWriteResponse := httptest.NewRecorder()
	handler.ServeHTTP(loopbackWriteResponse, loopbackWrite)
	if loopbackWriteResponse.Code != http.StatusForbidden {
		t.Fatalf("loopback write status = %d", loopbackWriteResponse.Code)
	}

	loopbackAPI := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/system", nil)
	loopbackAPI.RemoteAddr = "127.0.0.1:41234"
	loopbackAPIResponse := httptest.NewRecorder()
	handler.ServeHTTP(loopbackAPIResponse, loopbackAPI)
	if loopbackAPIResponse.Code != http.StatusOK {
		t.Fatalf("loopback read API status = %d", loopbackAPIResponse.Code)
	}

	externalAPI := httptest.NewRequest(http.MethodGet, "http://example.test/api/v1/system", nil)
	externalAPI.RemoteAddr = "203.0.113.20:41234"
	externalAPIResponse := httptest.NewRecorder()
	handler.ServeHTTP(externalAPIResponse, externalAPI)
	if externalAPIResponse.Code != http.StatusNotFound {
		t.Fatalf("external read API status = %d", externalAPIResponse.Code)
	}
}

func TestUnconfiguredAdminPreviewUsesTrustedProxyClientAddress(t *testing.T) {
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	server := New(
		config.Config{
			SiteTitle: "NewPress", PublicURL: "http://localhost:8080", ActiveTheme: "chunnel",
			TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("172.30.0.2/32")},
		},
		content.NewMemoryRepository(true), themes, plugin.NewManager(t.TempDir()),
	)
	handler := server.routes()

	external := httptest.NewRequest(http.MethodGet, "http://example.test/admin/", nil)
	external.RemoteAddr = "172.30.0.2:41234"
	external.Header.Set("X-Forwarded-For", "198.51.100.20")
	externalResponse := httptest.NewRecorder()
	handler.ServeHTTP(externalResponse, external)
	if externalResponse.Code != http.StatusNotFound {
		t.Fatalf("external proxy preview status = %d", externalResponse.Code)
	}

	local := httptest.NewRequest(http.MethodGet, "http://localhost/admin/", nil)
	local.RemoteAddr = "172.30.0.2:41234"
	local.Header.Set("X-Forwarded-For", "127.0.0.1")
	localResponse := httptest.NewRecorder()
	handler.ServeHTTP(localResponse, local)
	if localResponse.Code != http.StatusOK || !strings.Contains(localResponse.Body.String(), "本机只读预览") {
		t.Fatalf("local proxy preview status = %d", localResponse.Code)
	}

	forged := httptest.NewRequest(http.MethodGet, "http://example.test/admin/", nil)
	forged.RemoteAddr = "198.51.100.20:41234"
	forged.Header.Set("X-Forwarded-For", "127.0.0.1")
	forgedResponse := httptest.NewRecorder()
	handler.ServeHTTP(forgedResponse, forged)
	if forgedResponse.Code != http.StatusNotFound {
		t.Fatalf("forged proxy preview status = %d", forgedResponse.Code)
	}
}

func TestThemeAssetAndSettingsCSSValidators(t *testing.T) {
	t.Parallel()
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel")
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	server := New(config.Config{ActiveTheme: "chunnel"}, content.NewMemoryRepository(true), themes, plugin.NewManager(t.TempDir()))
	handler := server.routes()

	assetPath := coreui.AssetURL("css/chunnel-admin.css")
	assetRequest := httptest.NewRequest(http.MethodGet, assetPath, nil)
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, assetRequest)
	assetETag := assetResponse.Header().Get("ETag")
	assetBody := assetResponse.Body.String()
	if assetResponse.Code != http.StatusOK || assetETag == "" || assetResponse.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		!strings.Contains(assetBody, ".admin-content-select-all-copy") ||
		!strings.Contains(assetBody, ".admin-inventory-table.has-bulk-select tbody tr[data-content-row]") ||
		!strings.Contains(assetBody, ".admin-comment-bulk") ||
		!strings.Contains(assetBody, ".admin-comment-card.is-selected") ||
		!regexp.MustCompile(`display:\s*block;\s*border-collapse:\s*separate`).MatchString(assetBody) {
		t.Fatalf("versioned asset response = %d, etag=%q, cache=%q", assetResponse.Code, assetETag, assetResponse.Header().Get("Cache-Control"))
	}
	conditionalAsset := httptest.NewRequest(http.MethodGet, assetPath, nil)
	conditionalAsset.Header.Set("If-None-Match", `W/"other", `+assetETag)
	conditionalAssetResponse := httptest.NewRecorder()
	handler.ServeHTTP(conditionalAssetResponse, conditionalAsset)
	if conditionalAssetResponse.Code != http.StatusNotModified || conditionalAssetResponse.Header().Get("ETag") != assetETag || conditionalAssetResponse.Header().Get("Cache-Control") == "" {
		t.Fatalf("conditional asset response = %d, etag=%q, cache=%q", conditionalAssetResponse.Code, conditionalAssetResponse.Header().Get("ETag"), conditionalAssetResponse.Header().Get("Cache-Control"))
	}
	// Bulk controls belong to the list entry; the common shell no longer ships
	// comment/content business code on every admin route.
	adminJSPath := coreui.AssetURL("js/chunnel-admin-lists.js")
	adminJSRequest := httptest.NewRequest(http.MethodGet, adminJSPath, nil)
	adminJSResponse := httptest.NewRecorder()
	handler.ServeHTTP(adminJSResponse, adminJSRequest)
	adminJSBody := adminJSResponse.Body.String()
	if adminJSResponse.Code != http.StatusOK || adminJSResponse.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		!strings.Contains(adminJSBody, "[data-comment-bulk]") ||
		!strings.Contains(adminJSBody, "aria-selected") ||
		!regexp.MustCompile(`\.setAttribute\(["']aria-busy["'],\s*["']true["']\)`).MatchString(adminJSBody) {
		t.Fatalf("versioned admin JS response = %d, cache=%q", adminJSResponse.Code, adminJSResponse.Header().Get("Cache-Control"))
	}
	conditionalJS := httptest.NewRequest(http.MethodGet, adminJSPath, nil)
	conditionalJS.Header.Set("If-None-Match", adminJSResponse.Header().Get("ETag"))
	conditionalJSResponse := httptest.NewRecorder()
	handler.ServeHTTP(conditionalJSResponse, conditionalJS)
	if conditionalJSResponse.Code != http.StatusNotModified || conditionalJSResponse.Header().Get("ETag") == "" {
		t.Fatalf("conditional admin list JS response = %d", conditionalJSResponse.Code)
	}

	snapshot := themes.SettingsCSSSnapshot()
	cssPath := "/theme-settings.css?v=" + snapshot.Revision
	cssRequest := httptest.NewRequest(http.MethodGet, cssPath, nil)
	cssResponse := httptest.NewRecorder()
	handler.ServeHTTP(cssResponse, cssRequest)
	cssETag := cssResponse.Header().Get("ETag")
	if cssResponse.Code != http.StatusOK || cssETag == "" || !strings.Contains(cssResponse.Body.String(), "--cyan") || cssResponse.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("settings CSS response = %d, etag=%q, cache=%q", cssResponse.Code, cssETag, cssResponse.Header().Get("Cache-Control"))
	}
	conditionalCSS := httptest.NewRequest(http.MethodGet, cssPath, nil)
	conditionalCSS.Header.Set("If-None-Match", "*")
	conditionalCSSResponse := httptest.NewRecorder()
	handler.ServeHTTP(conditionalCSSResponse, conditionalCSS)
	if conditionalCSSResponse.Code != http.StatusNotModified || conditionalCSSResponse.Header().Get("ETag") != cssETag || conditionalCSSResponse.Header().Get("Cache-Control") == "" {
		t.Fatalf("conditional settings CSS response = %d, etag=%q, cache=%q", conditionalCSSResponse.Code, conditionalCSSResponse.Header().Get("ETag"), conditionalCSSResponse.Header().Get("Cache-Control"))
	}
}

func TestThemeAssetRangeHeadAndModifiedSince(t *testing.T) {
	t.Parallel()
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel")
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	server := New(config.Config{ActiveTheme: "chunnel"}, content.NewMemoryRepository(true), themes, plugin.NewManager(t.TempDir()))
	handler := server.routes()
	assetPath := themes.AssetURL("css/chunnel.css")

	fullRequest := httptest.NewRequest(http.MethodGet, assetPath, nil)
	fullResponse := httptest.NewRecorder()
	handler.ServeHTTP(fullResponse, fullRequest)
	etag := fullResponse.Header().Get("ETag")
	lastModified := fullResponse.Header().Get("Last-Modified")
	if fullResponse.Code != http.StatusOK || etag == "" || lastModified == "" || fullResponse.Body.Len() <= 32 {
		t.Fatalf("full asset response = %d, etag=%q, last-modified=%q, bytes=%d", fullResponse.Code, etag, lastModified, fullResponse.Body.Len())
	}

	rangeRequest := httptest.NewRequest(http.MethodGet, assetPath, nil)
	rangeRequest.Header.Set("Range", "bytes=0-31")
	rangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.Len() != 32 || !strings.HasPrefix(rangeResponse.Header().Get("Content-Range"), "bytes 0-31/") || rangeResponse.Header().Get("ETag") != etag {
		t.Fatalf("range asset response = %d, range=%q, etag=%q, bytes=%d", rangeResponse.Code, rangeResponse.Header().Get("Content-Range"), rangeResponse.Header().Get("ETag"), rangeResponse.Body.Len())
	}

	modifiedRequest := httptest.NewRequest(http.MethodGet, assetPath, nil)
	modifiedRequest.Header.Set("If-Modified-Since", lastModified)
	modifiedResponse := httptest.NewRecorder()
	handler.ServeHTTP(modifiedResponse, modifiedRequest)
	if modifiedResponse.Code != http.StatusNotModified || modifiedResponse.Body.Len() != 0 || modifiedResponse.Header().Get("ETag") != etag {
		t.Fatalf("modified asset response = %d, etag=%q, bytes=%d", modifiedResponse.Code, modifiedResponse.Header().Get("ETag"), modifiedResponse.Body.Len())
	}

	headRequest := httptest.NewRequest(http.MethodHead, assetPath, nil)
	headResponse := httptest.NewRecorder()
	handler.ServeHTTP(headResponse, headRequest)
	if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 || headResponse.Header().Get("Content-Length") == "" || headResponse.Header().Get("ETag") != etag {
		t.Fatalf("head asset response = %d, length=%q, etag=%q, bytes=%d", headResponse.Code, headResponse.Header().Get("Content-Length"), headResponse.Header().Get("ETag"), headResponse.Body.Len())
	}
}

func TestSafeAdminNext(t *testing.T) {
	t.Parallel()
	for candidate, expected := range map[string]string{
		"":                                "/admin/",
		"/admin/appearance/?tab=homepage": "/admin/appearance/?tab=homepage",
		"/admin/login/":                   "/admin/",
		"//attacker.test/admin/":          "/admin/",
		"https://attacker.test/admin/":    "/admin/",
		"/posts/example":                  "/admin/",
	} {
		if got := safeAdminNext(candidate); got != expected {
			t.Fatalf("safeAdminNext(%q) = %q, want %q", candidate, got, expected)
		}
	}
}

func TestETagMatches(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		header string
		want   bool
	}{
		{header: `"abc"`, want: true},
		{header: `W/"old", "abc"`, want: true},
		{header: `W/"abc"`, want: true},
		{header: `"other", W/"abc"`, want: true},
		{header: `W/"other"`, want: false},
		{header: `W/W/"abc"`, want: false},
		{header: `w/"abc"`, want: false},
		{header: `*`, want: true},
		{header: `"other"`, want: false},
		{header: ``, want: false},
	} {
		for _, current := range []string{`"abc"`, `W/"abc"`} {
			if got := etagMatches(test.header, current); got != test.want {
				t.Fatalf("etagMatches(%q, %q) = %v, want %v", test.header, current, got, test.want)
			}
		}
	}
}
