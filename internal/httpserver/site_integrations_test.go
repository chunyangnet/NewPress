package httpserver

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/site"
)

func TestSiteIntegrationsSaveRenderAndDisable(t *testing.T) {
	s, handler, _, cookie, principal := newAuthenticatedContentServer(t)
	get := func(path string) *httptest.ResponseRecorder {
		return serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, cookie, nil)
	}
	before := get("/")
	if strings.Contains(before.Body.String(), "data-site-integrations") || strings.Contains(before.Body.String(), "footer-compliance") {
		t.Fatal("default site loaded integrations")
	}
	form := siteSettingsForm(s.siteSettings.Snapshot(), principal.CSRFToken)
	for key, value := range map[string]string{
		"icp_number": "京ICP备12345678号-1", "police_number": "京公网安备11010102001234号", "police_code": "11010102001234",
		"analytics_enabled": "true", "analytics_google_id": "G-ABCDE12345", "analytics_google_tag_id": "GTM-ABCDE12",
		"analytics_baidu_id": strings.Repeat("a", 32), "analytics_clarity_id": "clarity123", "analytics_cloudflare_token": strings.Repeat("b", 32),
		"analytics_matomo_url": "https://stats.example.com/matomo", "analytics_matomo_site_id": "2",
		"analytics_umami_url": "https://cloud.umami.is/script.js", "analytics_umami_website_id": "94db1cb1-74f4-4a40-ad6c-962362670409",
		"analytics_plausible_url": "https://plausible.io/js/pa-example.js", "analytics_allowed_origins": "https://collect.example.com",
		"verification_google": "google-token", "verification_bing": "bing-token", "verification_baidu": "baidu-token",
		"verification_qihoo": "360-token", "verification_sogou": "sogou-token", "verification_yandex": "yandex-token",
		"verification_custom": `example-verification="><script>alert(1)</script>`,
	} {
		form.Set(key, value)
	}
	saved := serveContentForm(handler, cookie, "https://example.test/admin/settings/", "https://example.test", form)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
	}
	home := get("/")
	body, policy := home.Body.String(), home.Header().Get("Content-Security-Policy")
	for _, want := range []string{`data-cookie-consent`, `data-cookie-open`, `data-cookie-reject`, `data-cookie-accept`, `data-cookie-analytics`, `data-consent-required="true"`, `Cookie 设置`} {
		if !strings.Contains(body, want) {
			t.Errorf("cookie consent UI missing %q", want)
		}
	}
	for _, want := range []string{"京ICP备12345678号-1", "京公网安备11010102001234号", "https://beian.miit.gov.cn/", "https://beian.mps.gov.cn/#/query/webSearch?code=11010102001234", `name="google-site-verification" content="google-token"`, `name="msvalidate.01" content="bing-token"`, `name="baidu-site-verification"`, `name="360-site-verification"`, `name="sogou_site_verification"`, `name="yandex-verification"`, `name="example-verification"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, `<script>alert(1)</script>`) {
		t.Fatal("meta value escaped its attribute")
	}
	metaPos := strings.Index(body, `name="google-site-verification"`)
	if metaPos < 0 || metaPos > strings.Index(body, "</head>") {
		t.Fatal("verification outside head")
	}
	match := regexp.MustCompile(`data-site-integrations="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("missing config")
	}
	var config site.AnalyticsSettings
	if err := json.Unmarshal([]byte(html.UnescapeString(match[1])), &config); err != nil || !config.Active() || config.ClarityID != "clarity123" {
		t.Fatal("escaped config did not round trip", err)
	}
	for _, origin := range []string{"https://www.googletagmanager.com", "https://hm.baidu.com", "https://www.clarity.ms", "https://*.clarity.ms", "https://static.cloudflareinsights.com", "https://stats.example.com", "https://gateway.umami.is", "https://plausible.io", "https://collect.example.com", "'sha256-"} {
		if !strings.Contains(policy, origin) {
			t.Errorf("missing CSP source %q", origin)
		}
	}
	if strings.Contains(policy, "unsafe-inline") || strings.Contains(policy, "unsafe-eval") || !strings.Contains(policy, "object-src 'none'") {
		t.Fatal("CSP weakened", policy)
	}
	admin := get("/admin/settings/")
	if strings.Contains(admin.Body.String(), "data-site-integrations=") || strings.Contains(admin.Body.String(), `<meta name="google-site-verification"`) || strings.Contains(admin.Header().Get("Content-Security-Policy"), "clarity.ms") {
		t.Fatal("admin received tracking")
	}
	for _, label := range []string{"备案信息", "统计与跟踪", "站长平台认证", `name="analytics_clarity_id"`} {
		if !strings.Contains(admin.Body.String(), label) {
			t.Errorf("missing settings UI %s", label)
		}
	}
	loader := get(siteIntegrationScriptURL)
	if loader.Code != 200 || !strings.Contains(loader.Header().Get("Content-Type"), "javascript") || !strings.Contains(loader.Body.String(), "document.currentScript") {
		t.Fatal("loader not available")
	}
	conditional := httptest.NewRequest(http.MethodGet, "https://example.test"+siteIntegrationScriptURL, nil)
	conditional.Header.Set("If-None-Match", loader.Header().Get("ETag"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, conditional)
	if response.Code != 304 {
		t.Fatal("loader ETag ignored", response.Code)
	}
	form = siteSettingsForm(s.siteSettings.Snapshot(), principal.CSRFToken)
	form.Del("analytics_enabled")
	if saved := serveContentForm(handler, cookie, "https://example.test/admin/settings/", "https://example.test", form); saved.Code != 303 {
		t.Fatal("disable failed")
	}
	disabled := get("/")
	if strings.Contains(disabled.Body.String(), "data-site-integrations=") || strings.Contains(disabled.Header().Get("Content-Security-Policy"), "clarity.ms") {
		t.Fatal("disabled tracking remained active")
	}
	if !strings.Contains(disabled.Body.String(), "google-token") || s.siteSettings.Snapshot().Settings.IntegrationValues().Analytics.ClarityID != "clarity123" {
		t.Fatal("disable discarded settings")
	}
}

func TestCookieConsentCanBeDisabledInThemeSettings(t *testing.T) {
	s, handler, _, cookie, _ := newAuthenticatedContentServer(t)
	snap := s.siteSettings.Snapshot()
	snap.Settings.Integrations = &site.IntegrationsSettings{Analytics: site.AnalyticsSettings{Enabled: true, ClarityID: "consent123"}}
	if _, fields, err := s.siteSettings.Save(snap.Revision, snap.Settings); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		return serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, cookie, nil)
	}
	for _, enabled := range []bool{false, true} {
		page := get("/admin/appearance/")
		if page.Code != 200 || !strings.Contains(page.Body.String(), `name="cookie_consent_enabled"`) || !strings.Contains(page.Body.String(), "隐私与 Cookie") {
			t.Fatal("cookie settings missing from appearance form")
		}
		form := renderedAppearanceForm(t, page.Body.String())
		form.Del("cookie_consent_enabled")
		if enabled {
			form.Set("cookie_consent_enabled", "true")
		}
		form.Set("cookie_privacy_url", "/pages/privacy-policy/")
		if result := serveContentForm(handler, cookie, "https://example.test/admin/appearance/", "https://example.test", form); result.Code != 303 {
			t.Fatalf("save cookie settings: %d %s", result.Code, result.Body.String())
		}
		if err := s.themes.Load(); err != nil {
			t.Fatal(err)
		}
		home := get("/")
		body := home.Body.String()
		if home.Code != 200 || !strings.Contains(body, `data-site-integrations=`) {
			t.Fatal("analytics configuration was lost")
		}
		for _, marker := range []string{`data-cookie-consent`, `data-cookie-open`, `data-consent-required="true"`, `cookie-consent.js`, `cookie-consent.css`, `href="/pages/privacy-policy/"`} {
			if strings.Contains(body, marker) != enabled {
				t.Errorf("enabled=%v: unexpected presence of %s", enabled, marker)
			}
		}
		if enabled && strings.Index(body, "cookie-consent.js") > strings.Index(body, "data-site-integrations=") {
			t.Fatal("consent controller must initialize before analytics")
		}
	}
}

func TestCookieConsentAcrossPublicPagesAndPreviewExclusion(t *testing.T) {
	s, handler, repository, cookie, _ := newAuthenticatedContentServer(t)
	post, err := repository.Create(t.Context(), content.PostInput{Title: "Cookie test", Slug: "cookie-test", Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/explore/", "/posts/" + post.Slug, "/account/"} {
		page := serveCookieRequest(handler, "GET", "https://example.test"+path, cookie, nil)
		if page.Code != 200 || !strings.Contains(page.Body.String(), `data-cookie-consent`) {
			t.Errorf("consent missing from %s: %d", path, page.Code)
		}
	}
	for _, path := range []string{"/admin/appearance/", "/admin/appearance/preview/?page=home", "/admin/appearance/preview/?page=article"} {
		page := serveCookieRequest(handler, "GET", "https://example.test"+path, cookie, nil)
		if page.Code != 200 || strings.Contains(page.Body.String(), `data-cookie-consent`) || strings.Contains(page.Body.String(), `cookie-consent.js`) {
			t.Errorf("unexpected consent UI on %s: %d", path, page.Code)
		}
	}
	for _, path := range []string{"/theme-assets/js/cookie-consent.js", "/theme-assets/css/cookie-consent.css"} {
		page := serveCookieRequest(handler, "GET", "https://example.test"+path, nil, nil)
		if page.Code != 200 {
			t.Errorf("missing consent asset %s: %d", path, page.Code)
		}
	}
	if s.themes.SettingsSnapshot().Values["cookie_privacy_url"] != "" {
		t.Fatal("default privacy link must not point to a nonexistent page")
	}
}

func TestSiteIntegrationsExcludePrivateAndErrorViews(t *testing.T) {
	s, _, _, _, _ := newAuthenticatedContentServer(t)
	snap := s.siteSettings.Snapshot()
	snap.Settings.Integrations = &site.IntegrationsSettings{Analytics: site.AnalyticsSettings{Enabled: true, ClarityID: "test123"}, Verification: site.VerificationSettings{Google: "verify"}}
	if _, fields, err := s.siteSettings.Save(snap.Revision, snap.Settings); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	for _, tc := range []struct {
		page, class    string
		status         int
		admin, preview bool
	}{
		{"home", "is-home", 200, false, true}, {"admin", "is-admin", 200, true, false},
		{"account", "is-account", 200, false, false}, {"account-access", "is-account-access", 200, false, false},
		{"billing", "is-billing", 200, false, false},
		{"notification-unsubscribe", "", 200, false, false}, {"maintenance", "is-maintenance", 503, false, false},
		{"404", "is-error", 404, false, false}, {"home", "is-home", 302, false, false},
	} {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Security-Policy", "script-src 'self'")
		view := s.baseView("Home", "", "/")
		view.IsAdmin, view.IsPreview, view.BodyClass = tc.admin, tc.preview, tc.class
		s.prepareSiteIntegrations(w, tc.status, &view, tc.page)
		if view.Integrations.AnalyticsConfig != "" || len(view.Integrations.Verification) != 0 || w.Header().Get("Content-Security-Policy") != "script-src 'self'" {
			t.Errorf("integration leak: %#v", tc)
		}
	}
}
