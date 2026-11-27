package httpserver

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/module"
)

func TestModulesFullPathLifecycle(t *testing.T) {
	s, h, _, _, cookie, p := newPersistentAdminTestServer(t)
	endpoint := "https://example.test/api/v1/admin/modules"
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": p.CSRFToken}
	for _, path := range []string{"/admin/modules/", "/admin/appearance/layout/"} {
		r := serveContentAPI(h, cookie, "GET", "https://example.test"+path, "", nil)
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body.String())
		}
	}
	if r := serveContentAPI(h, nil, "GET", endpoint, "", nil); r.Code != 401 {
		t.Fatalf("catalog leaked: %d", r.Code)
	}
	initial := s.siteSettings.Snapshot()
	disabled := map[string]bool{}
	for _, d := range module.Catalog() {
		if d.Control == "switch" {
			disabled[d.ID] = false
		}
	}
	changes, _ := json.Marshal(disabled)
	body := fmt.Sprintf(`{"revision":%q,"modules":%s}`, initial.Revision, changes)
	r := serveContentAPI(h, cookie, "PATCH", endpoint, body, headers)
	if r.Code != 200 {
		t.Fatalf("patch %d %s", r.Code, r.Body.String())
	}
	// Exercise every registered method and path, including dynamic segments,
	// before it can reach a domain handler or mutate its retained data.
	placeholder := regexp.MustCompile(`\{[^}]+\}`)
	checked := 0
	for _, route := range s.moduleRoutes {
		if _, switchable := disabled[route.Module]; !switchable {
			continue
		}
		method, path, found := strings.Cut(route.Pattern, " ")
		if !found {
			method, path = "GET", route.Pattern
		}
		path = placeholder.ReplaceAllString(strings.ReplaceAll(path, "{$}", ""), "sample")
		r := serveContentAPI(h, cookie, method, "https://example.test"+path, "", nil)
		definition, _ := module.Lookup(route.Module)
		if (method == "GET" || method == "HEAD") && strings.HasPrefix(path, "/admin/") && path == definition.ConfigURL {
			if r.Code != 200 || (method == "GET" && !strings.Contains(r.Body.String(), "data-feature-status")) {
				t.Errorf("missing recovery page: %s = %d", route.Pattern, r.Code)
			}
			checked++
			continue
		}
		if r.Code != 404 || (strings.HasPrefix(path, "/api/") && !strings.Contains(r.Body.String(), `"module.disabled"`)) {
			t.Errorf("disabled registered route %s = %d %s", route.Pattern, r.Code, r.Body.String())
		}
		checked++
	}
	t.Logf("checked %d registered switch-owned routes", checked)
	if r := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/social/own-content?kind=comments", "", nil); r.Code != 404 {
		t.Fatalf("paused comments available through account content: %d", r.Code)
	}
	for _, path := range []string{"/api/v1/admin/comments", "/api/v1/posts/test/comments", "/api/v1/series", "/api/v1/community/circles", "/api/v1/admin/ai/prompts", "/admin/comments/1/edit/", "/series/", "/community/"} {
		r := serveContentAPI(h, cookie, "GET", "https://example.test"+path, "", nil)
		if r.Code != 404 {
			t.Errorf("disabled path %s = %d", path, r.Code)
		}
	}
	for _, path := range []string{"/", "/admin/", "/admin/content/new/", "/account/", "/admin/modules/"} {
		r := serveContentAPI(h, cookie, "GET", "https://example.test"+path, "", nil)
		if r.Code != 200 {
			t.Fatalf("core path disabled: %s %d", path, r.Code)
		}
		if path != "/admin/modules/" {
			for _, link := range []string{`href="/series/"`, `href="/community/"`, `href="/admin/comments/"`, `href="/admin/analytics/"`, `href="/admin/wechat/`, `data-library-tab="comments"`, `href="/admin/import/"`} {
				if at := strings.Index(r.Body.String(), link); at >= 0 {
					t.Log(r.Body.String()[max(0, at-130):min(r.Body.Len(), at+170)])
					t.Errorf("disabled navigation %s still visible on %s", link, path)
				}
			}
		}
	}
	if r := serveContentAPI(h, cookie, "PATCH", endpoint, body, headers); r.Code != 409 {
		t.Fatalf("stale settings save %d", r.Code)
	}
	// Ordinary site settings edits must preserve separately managed module policy.
	form := siteSettingsForm(s.siteSettings.Snapshot(), p.CSRFToken)
	form.Set("site_title", "Modules Site")
	if r := serveContentForm(h, cookie, "https://example.test/admin/settings/", "https://example.test", form); r.Code != 303 {
		t.Fatalf("ordinary settings %d %s", r.Code, r.Body.String())
	}
	if s.moduleEnabled("comments") {
		t.Fatal("ordinary settings reset module policy")
	}
	body = fmt.Sprintf(`{"revision":%q,"modules":{"comments":true,"series":true}}`, s.siteSettings.Snapshot().Revision)
	if r := serveContentAPI(h, cookie, "PATCH", endpoint, body, headers); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r := serveContentAPI(h, cookie, "GET", "https://example.test/admin/comments/", "", nil); r.Code != 200 {
		t.Fatal("module did not recover", r.Code)
	}
	var result struct {
		Items []moduleView `json:"items"`
	}
	r = serveContentAPI(h, cookie, "GET", endpoint, "", nil)
	if json.Unmarshal(r.Body.Bytes(), &result) != nil || len(result.Items) != len(module.Catalog()) {
		t.Fatal("catalog incomplete")
	}
	registered := 0
	for _, v := range result.Items {
		registered += len(v.Routes)
	}
	if registered != len(s.moduleRoutes) || registered < 100 {
		t.Fatal("route inventory incomplete")
	}
}

func TestModuleAvailabilityExplainsServerRequirements(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	s.config.CommunityEnabled, s.config.AnalyticsEnabled = false, false
	for _, item := range s.moduleCatalog(map[string]bool{"community": true, "analytics": true}) {
		if item.ID != "community" && item.ID != "analytics" {
			continue
		}
		if !item.Enabled || item.Available || item.State != "unavailable" || !strings.Contains(item.Reason, "NEWPRESS_") {
			t.Fatalf("misleading runtime status: %+v", item)
		}
	}
}
func TestModulesRejectInvalidRequests(t *testing.T) {
	s, h, _, cookie, p := newAuthenticatedContentServer(t)
	endpoint := "https://example.test/api/v1/admin/modules"
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": p.CSRFToken}
	before := s.siteSettings.Snapshot().Revision
	for _, tc := range []struct {
		changes string
		status  int
	}{{`{"unknown":true}`, 422}, {`{"plugin-jobs":true}`, 422}, {`{"currency":false}`, 422}, {`{"comments":null}`, 400}, {`{"comments":true,"comments":false}`, 400}, {`{}`, 400}} {
		body := fmt.Sprintf(`{"revision":%q,"modules":%s}`, before, tc.changes)
		if r := serveContentAPI(h, cookie, "PATCH", endpoint, body, headers); r.Code != tc.status {
			t.Errorf("%s: %d %s", tc.changes, r.Code, r.Body.String())
		}
	}
	headers["Origin"] = "https://other.test"
	if r := serveContentAPI(h, cookie, "PATCH", endpoint, fmt.Sprintf(`{"revision":%q,"modules":{"comments":false}}`, before), headers); r.Code != 403 {
		t.Fatal("cross-site write accepted")
	}
	if s.siteSettings.Snapshot().Revision != before {
		t.Fatal("rejected request persisted changes")
	}
}
func TestLayoutSavePreviewConflictAndValidation(t *testing.T) {
	s, h, _, cookie, p := newAuthenticatedContentServer(t)
	before := s.themes.SettingsSnapshot()
	form := url.Values{"csrf_token": {p.CSRFToken}, "revision": {before.Revision}}
	for key, value := range before.Values {
		form.Set(key, fmt.Sprint(value))
	}
	form.Set("home_top_order", "notice,channels,categories,recent,welcome")
	form.Set("home_main_order", "series,spotlight,community")
	form.Set("home_aside_order", "hero,feed,checkin,authors,tags,stats,subscribe")
	form.Set("home_recent_visibility", "desktop")
	preview := serveContentForm(h, cookie, "https://example.test/admin/appearance/preview/?page=home", "https://example.test", form)
	if preview.Code != 200 || s.themes.SettingsSnapshot().Revision != before.Revision {
		t.Fatal("preview changed settings", preview.Code, preview.Body.String())
	}
	var candidate struct {
		HTML string `json:"html"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &candidate); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"hero", "feed", "welcome"} {
		pattern := regexp.MustCompile(`<section[^>]*data-home-module="` + key + `"[^>]*>\s*<(div|article|nav)\b`)
		if !pattern.MatchString(candidate.HTML) {
			t.Errorf("moved block %s has no rendered content", key)
		}
	}
	endpoint := "https://example.test/admin/appearance/layout/"
	r := serveContentForm(h, cookie, endpoint, "https://example.test", form)
	if r.Code != 303 {
		t.Fatalf("save %d %s", r.Code, r.Body.String())
	}
	if s.themes.SettingsSnapshot().Values["home_top_order"] != form.Get("home_top_order") {
		t.Fatal("cross-region order not saved")
	}
	if r = serveContentForm(h, cookie, endpoint, "https://example.test", form); r.Code != 409 {
		t.Fatal("stale layout overwrite accepted", r.Code)
	}
	form.Set("revision", s.themes.SettingsSnapshot().Revision)
	form.Set("home_main_order", "feed,recent")
	if r = serveContentForm(h, cookie, endpoint, "https://example.test", form); r.Code != 422 {
		t.Fatal("duplicate placement accepted", r.Code)
	}
	form.Set("home_main_order", "series,spotlight,community")
	form.Add("home_main_order", "feed")
	if r = serveContentForm(h, cookie, endpoint, "https://example.test", form); r.Code != 400 {
		t.Fatal("ambiguous form accepted", r.Code)
	}
}
