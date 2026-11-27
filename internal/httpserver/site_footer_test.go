package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"newpress.local/newpress/internal/coreui"
)

func TestSiteFooterSaveRendersHomePreviewAndCorePages(t *testing.T) {
	s, handler, _, cookie, principal := newAuthenticatedContentServer(t)
	before := serveCookieRequest(handler, http.MethodGet, "https://example.test/", cookie, nil)
	if strings.Contains(before.Body.String(), "chunnel-footer.css") {
		t.Fatal("default site loaded custom footer styles")
	}
	form := siteSettingsForm(s.siteSettings.Snapshot(), principal.CSRFToken)
	form.Set("icp_number", "京ICP备12345678号-1")
	form.Set("police_number", "京公网安备11010102001234号")
	form.Set("police_code", "11010102001234")
	form.Set("footer_copyright", "© 2026 测试社区")
	form.Set("footer_html", `<p>一起分享 <a href="/privacy/">隐私政策</a></p><script>alert(1)</script>`)
	form.Set("footer_hide_credit", "true")
	form.Set("analytics_enabled", "true")
	form.Set("analytics_clarity_id", "footer123")
	response := serveContentForm(handler, cookie, "https://example.test/admin/settings/", "https://example.test", form)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", response.Code, response.Body.String())
	}
	checkFooter := func(body string) {
		t.Helper()
		for _, expected := range []string{"chunnel-footer.css", `id="site-footer"`, "© 2026 测试社区", `class="footer-custom"`, `href="/privacy/"`, "京ICP备12345678号-1", "京公网安备11010102001234号", "https://beian.miit.gov.cn/", "code=11010102001234", "police-record-badge.png"} {
			if !strings.Contains(body, expected) {
				t.Errorf("footer missing %s", expected)
			}
		}
		if strings.Contains(body, "由 NewPress 提供支持") || strings.Contains(body, "<script>alert(1)</script>") {
			t.Error("footer contains removed credit or executable HTML")
		}
	}
	for _, style := range []string{"dark", "paper", "minimal"} {
		settings := s.themes.SettingsSnapshot().Values
		settings["footer_style"] = style
		if _, fields, err := s.themes.SaveSettings(settings); err != nil || len(fields) != 0 {
			t.Fatal(fields, err)
		}
		home := serveCookieRequest(handler, http.MethodGet, "https://example.test/", cookie, nil)
		if home.Code != http.StatusOK {
			t.Fatalf("home: %d", home.Code)
		}
		checkFooter(home.Body.String())
	}
	preview := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/appearance/preview/?page=home", cookie, nil)
	var result map[string]string
	if preview.Code != http.StatusOK || json.Unmarshal(preview.Body.Bytes(), &result) != nil {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	checkFooter(result["html"])
	if strings.Contains(result["html"], "data-site-integrations=") {
		t.Fatal("preview enabled tracking")
	}
	core := s.baseView("登录", "", "/account/access/")
	s.prepareSiteIntegrations(httptest.NewRecorder(), http.StatusOK, &core, "account-access")
	if core.Integrations.FooterCopyright != "© 2026 测试社区" || publicPage(core).Integrations.FooterHTML == "" {
		t.Fatal("footer missing from core/public theme adapter")
	}
	core.ThemeValues, core.ThemeClass = coreui.Defaults()
	core.UIAssetVersion = coreui.Revision()
	core.BodyClass = "is-account-access"
	var corePage strings.Builder
	if err := coreui.Render(&corePage, "account-access", core); err != nil {
		t.Fatal("core footer render", err)
	}
	checkFooter(corePage.String())
	coreStyles := coreui.AssetURL("css/chunnel-footer.css")
	if !strings.Contains(corePage.String(), coreStyles) {
		t.Fatal("core footer styles missing")
	}
	asset := serveCookieRequest(handler, http.MethodGet, "https://example.test"+coreStyles, nil, nil)
	if asset.Code != http.StatusOK || !strings.Contains(asset.Body.String(), ".footer-custom") {
		t.Fatal("core footer styles unavailable")
	}
	admin := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/settings/?notice=saved", cookie, nil)
	for _, text := range []string{`name="footer_copyright"`, `name="footer_html"`, `name="footer_hide_credit"`, "备案与页脚内容已更新", "查看首页页脚"} {
		if !strings.Contains(admin.Body.String(), text) {
			t.Errorf("settings missing %s", text)
		}
	}
	form = siteSettingsForm(s.siteSettings.Snapshot(), principal.CSRFToken)
	for _, key := range []string{"icp_number", "police_number", "police_code", "footer_copyright", "footer_html", "footer_hide_credit"} {
		form.Del(key)
	}
	if response := serveContentForm(handler, cookie, "https://example.test/admin/settings/", "https://example.test", form); response.Code != http.StatusSeeOther {
		t.Fatal("clear failed", response.Code)
	}
	home := serveCookieRequest(handler, http.MethodGet, "https://example.test/", cookie, nil).Body.String()
	if strings.Contains(home, "chunnel-footer.css") || strings.Contains(home, `class="footer-custom"`) || strings.Contains(home, `class="footer-compliance"`) || !strings.Contains(home, "由 NewPress 提供支持") {
		t.Fatal("cleared footer remained visible")
	}
}
