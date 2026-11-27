package httpserver

import (
	"encoding/json"
	"html"
	"net/http"
	"newpress.local/newpress/internal/analytics"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAnalyticsBootstrapMatchesAuthorizedReport(t *testing.T) {
	_, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	for _, days := range []string{"7", "30", "90"} {
		page := serveContentAPI(handler, cookie, http.MethodGet, "https://example.test/admin/analytics/?days="+days, "", nil)
		if page.Code != http.StatusOK {
			t.Fatal(page.Code)
		}
		match := regexp.MustCompile(`data-analytics-report="([^"]+)"`).FindStringSubmatch(page.Body.String())
		if len(match) != 2 {
			t.Fatal("missing safely escaped report bootstrap")
		}
		var bootstrap, api analyticsView
		if err := json.Unmarshal([]byte(html.UnescapeString(match[1])), &bootstrap); err != nil {
			t.Fatal(err)
		}
		response := serveContentAPI(handler, cookie, http.MethodGet, "https://example.test/api/v1/admin/analytics?days="+days, "", nil)
		if err := json.Unmarshal(response.Body.Bytes(), &api); err != nil {
			t.Fatal(err)
		}
		if bootstrap.Days != api.Days || bootstrap.Timezone != api.Timezone || bootstrap.Start != api.Start || bootstrap.End != api.End || bootstrap.Views != api.Views || len(bootstrap.Trend) != api.Days {
			t.Fatalf("bootstrap differs from API: %+v / %+v", bootstrap, api)
		}
		if !strings.Contains(page.Body.String(), "data-analytics-fallback") || !strings.Contains(page.Body.String(), "查看每日明细") {
			t.Fatal("native report must remain available before JavaScript")
		}
	}
}

func TestAnalyticsTrialAssetsAreRouteLocal(t *testing.T) {
	_, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	for _, path := range []string{"/", "/explore/", "/admin/", "/admin/content/", "/admin/analytics/ai/"} {
		response := serveContentAPI(handler, cookie, http.MethodGet, "https://example.test"+path, "", nil)
		if strings.Contains(response.Body.String(), "newpress-analytics") {
			t.Fatal("analytics dependency leaked into", path)
		}
	}
}

func TestAnalyticsCollectionPrivacyAndAuthorization(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	s.config.AnalyticsEnabled = true
	reader, readerCookie, _ := createAndLoginTestUser(t, manager, owner, "analytics-reader", "analytics-reader@example.test", "统计读者", "subscriber")
	public, err := s.repository.Create(t.Context(), content.PostInput{Title: "阅读观察文章", Slug: "analytics-public", Status: content.StatusPublish, AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.engagements.Set(t.Context(), engagement.Input{UserID: reader.ID, TargetID: public.ID, Kind: engagement.Like, Active: true}); err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"User-Agent": "Mozilla/5.0 Chrome/120 Safari/537.36", "Sec-Fetch-Dest": "document", "Referer": "https://search.example/find?secret=private"}
	base := "https://example.test"
	for _, path := range []string{"/posts/analytics-public", "/explore/?q=unmatched-reading-topic", "/explore/?q=private%40example.test", "/"} {
		r := serveContentAPI(handler, nil, http.MethodGet, base+path, "", headers)
		if r.Code != 200 {
			t.Fatal(path, r.Code, r.Body.String())
		}
	}
	for _, override := range []map[string]string{{"User-Agent": "Googlebot"}, {"User-Agent": ""}, {"DNT": "1"}, {"Sec-GPC": "1"}, {"Sec-Purpose": "prefetch;prerender"}, {"Sec-Fetch-Dest": "empty"}} {
		h := map[string]string{}
		for k, v := range headers {
			h[k] = v
		}
		for k, v := range override {
			h[k] = v
		}
		serveContentAPI(handler, nil, http.MethodGet, base+"/posts/analytics-public", "", h)
	}
	serveContentAPI(handler, cookie, http.MethodGet, base+"/posts/analytics-public", "", headers)
	serveContentAPI(handler, nil, http.MethodHead, base+"/posts/analytics-public", "", headers)
	serveContentAPI(handler, nil, http.MethodGet, base+"/posts/missing-analytics-article", "", headers)
	serveContentAPI(handler, nil, http.MethodGet, base+"/api/v1/posts", "", headers)
	s.config.AnalyticsEnabled = false
	serveContentAPI(handler, nil, http.MethodGet, base+"/", "", headers)
	s.config.AnalyticsEnabled = true
	url := base + "/api/v1/admin/analytics?days=7"
	if r := serveContentAPI(handler, nil, http.MethodGet, url, "", nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
	if r := serveContentAPI(handler, readerCookie, http.MethodGet, url, "", nil); r.Code != 403 {
		t.Fatal(r.Code)
	}
	r := serveContentAPI(handler, cookie, http.MethodGet, url, "", nil)
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(r.Code, r.Body.String())
	}
	var report analyticsView
	if err = json.Unmarshal(r.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Timezone != "Asia/Shanghai" || report.Views != 4 || report.ArticleViews != 1 || report.Searches != 2 || report.Misses != 2 || len(report.Articles) != 1 || report.Articles[0].Likes != 1 || len(report.Trend) != 7 || len(report.Terms) != 1 {
		t.Fatalf("incorrect report %+v", report)
	}
	for _, private := range []string{"secret=private", "private@example.test", "writers", "user_agent", "ip_address"} {
		if strings.Contains(r.Body.String(), private) {
			t.Fatal("private information leaked", private)
		}
	}
	for _, query := range []string{"days=0", "days=8", "days=7&days=30", "unknown=yes"} {
		if r := serveContentAPI(handler, cookie, http.MethodGet, base+"/api/v1/admin/analytics?"+query, "", nil); r.Code != 400 {
			t.Fatal(query, r.Code)
		}
	}
	if r := serveContentAPI(handler, cookie, http.MethodGet, base+"/admin/analytics/?days=7", "", nil); r.Code != 200 || !strings.Contains(r.Body.String(), "访问趋势") || !strings.Contains(r.Body.String(), "阅读观察文章") || !strings.Contains(r.Body.String(), "</html>") {
		t.Fatal(r.Code, r.Body.String())
	}
	input := content.PostInput{Type: public.Type, Title: public.Title, Slug: public.Slug, AuthorID: public.AuthorID, PublishedAt: public.PublishedAt}
	input.Status = content.StatusPrivate
	if _, err = s.repository.Update(t.Context(), public.ID, content.Mutation{ExpectedVersion: public.Version}, input); err != nil {
		t.Fatal(err)
	}
	report, err = s.analyticsReport(t.Context(), 7)
	if err != nil || len(report.Articles) != 0 || report.Views != 4 {
		t.Fatal("withdrawal leaked or erased totals", report, err)
	}
	state, err := s.analytics.Snapshot(t.Context(), time.Now())
	if err != nil || len(state.Days) != 1 {
		t.Fatal(err)
	}
}

func TestAnalyticsUnavailableDoesNotReportSuccessfulZero(t *testing.T) {
	s, handler, _, _, cookie, _ := newPersistentAdminTestServer(t)
	path := filepath.Join(t.TempDir(), "analytics.json")
	store, err := analytics.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	collector := analytics.NewCollector(store)
	collector.Record(analytics.Event{At: time.Now(), Source: "direct"})
	if err = collector.Flush(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.SetAnalytics(collector)
	if err = os.WriteFile(path, []byte(`{"private":"error-details"}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := serveContentAPI(handler, cookie, http.MethodGet, "https://example.test/api/v1/admin/analytics", "", nil)
	if r.Code != 503 || strings.Contains(r.Body.String(), "error-details") || strings.Contains(r.Body.String(), path) {
		t.Fatal(r.Code, r.Body.String())
	}
}
