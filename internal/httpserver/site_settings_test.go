package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"newpress.local/newpress/internal/site"
)

func TestAdminSiteSettingsWorkflowAndRuntimeIntegration(t *testing.T) {
	server, handler, _, cookie, principal := newAuthenticatedContentServer(t)

	pageRequest := httptest.NewRequest(http.MethodGet, "https://example.test/admin/settings/", nil)
	pageRequest.AddCookie(cookie)
	pageResponse := httptest.NewRecorder()
	handler.ServeHTTP(pageResponse, pageRequest)
	pageBody := pageResponse.Body.String()
	if pageResponse.Code != http.StatusOK || !strings.Contains(pageBody, "<h1>站点设置</h1>") ||
		!strings.Contains(pageBody, `name="revision"`) || !strings.Contains(pageBody, `data-admin-savebar`) {
		t.Fatalf("settings page = %d %s", pageResponse.Code, pageBody)
	}

	initial := server.siteSettings.Snapshot()
	form := siteSettingsForm(initial, principal.CSRFToken)
	form.Set("site_title", "隧道出版")
	form.Set("site_tagline", "让内容安全抵达读者")
	form.Set("site_description", "一个面向中文创作者的高性能内容平台")
	form.Set("default_content_status", "pending")
	form.Set("default_category", "编辑精选")
	form.Del("post_comments_enabled")
	form.Set("home_posts", "12")
	form.Del("public_posts_enabled")
	form.Del("admin_content_api_enabled")
	form.Set("security_contact", "mailto:security@example.test")
	saved := serveContentForm(handler, cookie, "https://example.test/admin/settings/", "https://example.test", form)
	if saved.Code != http.StatusSeeOther || saved.Header().Get("Location") != "/admin/settings/?notice=saved" {
		t.Fatalf("save settings = %d %q %s", saved.Code, saved.Header().Get("Location"), saved.Body.String())
	}
	afterSave := server.siteSettings.Snapshot()
	if afterSave.Revision == initial.Revision || afterSave.Settings.Identity.Title != "隧道出版" ||
		afterSave.Settings.Writing.DefaultStatus != "pending" || afterSave.Settings.API.PublicPostsEnabled {
		t.Fatalf("saved snapshot = %#v", afterSave)
	}

	homeRequest := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
	homeResponse := httptest.NewRecorder()
	handler.ServeHTTP(homeResponse, homeRequest)
	if homeResponse.Code != http.StatusOK || !strings.Contains(homeResponse.Body.String(), "<title>隧道出版</title>") ||
		!strings.Contains(homeResponse.Body.String(), `lang="zh-CN"`) {
		t.Fatalf("runtime identity = %d %s", homeResponse.Code, homeResponse.Body.String())
	}

	publicAPIRequest := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/posts", nil)
	publicAPIResponse := httptest.NewRecorder()
	handler.ServeHTTP(publicAPIResponse, publicAPIRequest)
	if publicAPIResponse.Code != http.StatusNotFound {
		t.Fatalf("disabled public API = %d %s", publicAPIResponse.Code, publicAPIResponse.Body.String())
	}
	adminAPIRequest := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/admin/contents", nil)
	adminAPIRequest.AddCookie(cookie)
	adminAPIResponse := httptest.NewRecorder()
	handler.ServeHTTP(adminAPIResponse, adminAPIRequest)
	if adminAPIResponse.Code != http.StatusNotFound || !strings.Contains(adminAPIResponse.Body.String(), "request.not_found") {
		t.Fatalf("disabled admin API = %d %s", adminAPIResponse.Code, adminAPIResponse.Body.String())
	}

	newPostRequest := httptest.NewRequest(http.MethodGet, "https://example.test/admin/content/new/", nil)
	newPostRequest.AddCookie(cookie)
	newPostResponse := httptest.NewRecorder()
	handler.ServeHTTP(newPostResponse, newPostRequest)
	newPostBody := newPostResponse.Body.String()
	if newPostResponse.Code != http.StatusOK || !strings.Contains(newPostBody, `<option value="pending" selected`) ||
		!strings.Contains(newPostBody, `value="编辑精选"`) || !strings.Contains(newPostBody, `<option value="closed" selected`) {
		t.Fatalf("writing defaults = %d %s", newPostResponse.Code, newPostBody)
	}

	securityRequest := httptest.NewRequest(http.MethodGet, "https://example.test/.well-known/security.txt", nil)
	securityResponse := httptest.NewRecorder()
	handler.ServeHTTP(securityResponse, securityRequest)
	if securityResponse.Code != http.StatusOK || !strings.Contains(securityResponse.Body.String(), "Contact: mailto:security@example.test") ||
		!strings.Contains(securityResponse.Body.String(), "Canonical: https://example.test/.well-known/security.txt") {
		t.Fatalf("security.txt = %d %s", securityResponse.Code, securityResponse.Body.String())
	}
	sitemapRequest := httptest.NewRequest(http.MethodGet, "https://example.test/sitemap.xml", nil)
	sitemapResponse := httptest.NewRecorder()
	handler.ServeHTTP(sitemapResponse, sitemapRequest)
	if sitemapResponse.Code != http.StatusOK || sitemapResponse.Header().Get("ETag") == "" ||
		!strings.Contains(sitemapResponse.Body.String(), "https://example.test/explore/") {
		t.Fatalf("sitemap = %d %#v %s", sitemapResponse.Code, sitemapResponse.Header(), sitemapResponse.Body.String())
	}
	conditionalSitemap := httptest.NewRequest(http.MethodGet, "https://example.test/sitemap.xml", nil)
	conditionalSitemap.Header.Set("If-None-Match", sitemapResponse.Header().Get("ETag"))
	conditionalSitemapResponse := httptest.NewRecorder()
	handler.ServeHTTP(conditionalSitemapResponse, conditionalSitemap)
	if conditionalSitemapResponse.Code != http.StatusNotModified || conditionalSitemapResponse.Body.Len() != 0 {
		t.Fatalf("conditional sitemap = %d %s", conditionalSitemapResponse.Code, conditionalSitemapResponse.Body.String())
	}

	stale := siteSettingsForm(initial, principal.CSRFToken)
	stale.Set("site_title", "过期写入")
	staleResponse := serveContentForm(handler, cookie, "https://example.test/admin/settings/", "https://example.test", stale)
	if staleResponse.Code != http.StatusConflict || !strings.Contains(staleResponse.Body.String(), "设置已被其他会话更新") {
		t.Fatalf("stale settings = %d %s", staleResponse.Code, staleResponse.Body.String())
	}

	maintenance := afterSave.Settings
	maintenance.Operations.MaintenanceEnabled = true
	maintenance.Operations.MaintenanceMessage = "预计 30 分钟后恢复服务"
	maintenanceSnapshot, fields, err := server.siteSettings.Save(afterSave.Revision, maintenance)
	if err != nil || len(fields) != 0 {
		t.Fatalf("enable maintenance fields=%v err=%v", fields, err)
	}
	if !maintenanceSnapshot.Settings.Operations.MaintenanceEnabled {
		t.Fatal("maintenance setting was not applied")
	}

	exploreRequest := httptest.NewRequest(http.MethodGet, "https://example.test/explore/", nil)
	exploreResponse := httptest.NewRecorder()
	handler.ServeHTTP(exploreResponse, exploreRequest)
	if exploreResponse.Code != http.StatusServiceUnavailable || exploreResponse.Header().Get("Retry-After") != "300" ||
		!strings.Contains(exploreResponse.Body.String(), "预计 30 分钟后恢复服务") {
		t.Fatalf("maintenance page = %d %#v %s", exploreResponse.Code, exploreResponse.Header(), exploreResponse.Body.String())
	}

	adminDuringMaintenance := httptest.NewRequest(http.MethodGet, "https://example.test/admin/settings/", nil)
	adminDuringMaintenance.AddCookie(cookie)
	adminDuringMaintenanceResponse := httptest.NewRecorder()
	handler.ServeHTTP(adminDuringMaintenanceResponse, adminDuringMaintenance)
	if adminDuringMaintenanceResponse.Code != http.StatusOK {
		t.Fatalf("admin during maintenance = %d %s", adminDuringMaintenanceResponse.Code, adminDuringMaintenanceResponse.Body.String())
	}

	faviconDuringMaintenance := httptest.NewRequest(http.MethodGet, "https://example.test/favicon.ico", nil)
	faviconDuringMaintenanceResponse := httptest.NewRecorder()
	handler.ServeHTTP(faviconDuringMaintenanceResponse, faviconDuringMaintenance)
	if faviconDuringMaintenanceResponse.Code != http.StatusTemporaryRedirect || faviconDuringMaintenanceResponse.Header().Get("Location") == "" {
		t.Fatalf("favicon during maintenance = %d %q", faviconDuringMaintenanceResponse.Code, faviconDuringMaintenanceResponse.Header().Get("Location"))
	}

	robotsRequest := httptest.NewRequest(http.MethodGet, "https://example.test/robots.txt", nil)
	robotsResponse := httptest.NewRecorder()
	handler.ServeHTTP(robotsResponse, robotsRequest)
	if robotsResponse.Code != http.StatusOK || !strings.Contains(robotsResponse.Body.String(), "Disallow: /") {
		t.Fatalf("maintenance robots = %d %s", robotsResponse.Code, robotsResponse.Body.String())
	}
}

func TestAdminSiteSettingsRejectsUnknownAndCrossSiteForms(t *testing.T) {
	server, handler, _, cookie, principal := newAuthenticatedContentServer(t)
	form := siteSettingsForm(server.siteSettings.Snapshot(), principal.CSRFToken)
	form.Set("unknown_capability", "administrator")
	unknown := serveContentForm(handler, cookie, "https://example.test/admin/settings/", "https://example.test", form)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown setting = %d %s", unknown.Code, unknown.Body.String())
	}

	valid := siteSettingsForm(server.siteSettings.Snapshot(), principal.CSRFToken)
	crossSite := serveContentForm(handler, cookie, "https://example.test/admin/settings/", "https://attacker.test", valid)
	if crossSite.Code != http.StatusForbidden {
		t.Fatalf("cross-site setting = %d %s", crossSite.Code, crossSite.Body.String())
	}
}

func siteSettingsForm(snapshot site.SettingsSnapshot, csrfToken string) url.Values {
	values := url.Values{
		"csrf_token":             {csrfToken},
		"revision":               {snapshot.Revision},
		"action":                 {"save"},
		"account_policy_version": {"2"},
	}
	for _, group := range snapshot.Groups {
		for _, field := range group.Fields {
			if field.Control == "toggle" {
				if field.BoolValue {
					values.Set(field.Key, "true")
				}
				continue
			}
			values.Set(field.Key, field.StringValue)
		}
	}
	return values
}
