package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/plugin"
)

func TestPluginMarketSourceDiagnosticsRenderSafely(t *testing.T) {
	s, _, _, _, p := newCommentHTTPTestServer(t)
	data := s.baseView("官方插件", "按需安装官方扩展。", "/admin/plugins/")
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-plugins"
	data.Admin = s.adminView("plugins", p, false)
	data.PluginAdmin = pluginAdminView{Market: plugin.MarketView{
		Configured: true,
		Source:     "https://gitee.com/official/plugins/raw/main/catalog.json",
		Sources: []plugin.MarketSourceStatus{
			{Name: "GitHub", Error: "<script>untrusted</script>"},
			{Name: "Gitee", Available: true, Selected: true, LatencyMS: 42},
		},
	}}
	w := httptest.NewRecorder()
	s.render(w, http.StatusOK, "admin-plugins", data)
	body := w.Body.String()
	for _, text := range []string{"GitHub", "Gitee", "当前使用", "42 ms", "&lt;script&gt;untrusted&lt;/script&gt;"} {
		if !strings.Contains(body, text) {
			t.Errorf("missing source diagnostic %q", text)
		}
	}
	if w.Code != http.StatusOK || strings.Contains(body, "<script>untrusted</script>") {
		t.Fatal("unsafe or failed source diagnostics render")
	}
}

func TestPluginMarketAutomaticReadRetainsAuthAndQueryBoundaries(t *testing.T) {
	_, handler, _, cookie, _ := newCommentHTTPTestServer(t)
	page := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/plugins/?market=1", "", "", "8.8.8.8:80")
	if page.Code != http.StatusOK || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("automatic market read: %d %s", page.Code, page.Body.String())
	}
	for _, marker := range []string{"data-plugin-market-content", "data-market-configured=", "data-readonly=", "data-plugin-market-loading", "data-plugin-loading-title", "data-plugin-loading-detail"} {
		if !strings.Contains(page.Body.String(), marker) {
			t.Errorf("automatic market page missing %s", marker)
		}
	}
	for _, query := range []string{"market=2", "market=1&settings=comment-origin", "market=1&market=1", "market=1&unknown=1"} {
		response := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/plugins/?"+query, "", "", "8.8.8.8:80")
		if response.Code != http.StatusBadRequest {
			t.Errorf("ambiguous automatic market query accepted %s: %d", query, response.Code)
		}
	}
	unauthorized := serveCommentRequest(handler, nil, http.MethodGet, "https://example.test/admin/plugins/?market=1", "", "", "8.8.8.8:80")
	if unauthorized.Code == http.StatusOK || unauthorized.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthorized automatic market read: %d", unauthorized.Code)
	}
}

func TestPluginAdminCanRemoveDamagedInstallation(t *testing.T) {
	s, h, _, cookie, p := newCommentHTTPTestServer(t)
	root := t.TempDir()
	s.plugins = plugin.NewManager(root)
	if err := s.plugins.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, plugin.CommentOrigin, "plugin.json"), []byte(`{broken`), 0600); err != nil {
		t.Fatal(err)
	}
	w := serveCommentRequest(h, cookie, http.MethodGet, "https://example.test/admin/plugins/", "", "", "8.8.8.8:80")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `data-plugin-remove="comment-origin"`) || !strings.Contains(w.Body.String(), "需修复") {
		t.Fatalf("damaged card has no usable removal action: %d %s", w.Code, w.Body.String())
	}
	b, _ := json.Marshal(map[string]any{"action": "uninstall", "slug": plugin.CommentOrigin, "expected_user_id": p.UserID})
	w = serveAdminCommentAPI(h, cookie, p.CSRFToken, http.MethodPost, "https://example.test/api/v1/admin/plugins", string(b), "", "https://example.test")
	if w.Code != 200 {
		t.Fatalf("uninstall failed: %d %s", w.Code, w.Body.String())
	}
}

func TestOfficialPluginsAdminLifecycleAndCommentPrivacy(t *testing.T) {
	s, h, comments, cookie, p := newCommentHTTPTestServer(t)
	action := func(operation, slug, csrf string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"action": operation, "slug": slug, "expected_user_id": p.UserID})
		return serveAdminCommentAPI(h, cookie, csrf, http.MethodPost, "https://example.test/api/v1/admin/plugins", string(b), "", "https://example.test")
	}
	if w := action("install", plugin.CommentOrigin, ""); w.Code != 403 {
		t.Fatalf("missing CSRF %d %s", w.Code, w.Body.String())
	}
	if w := action("install", plugin.CommentOrigin, p.CSRFToken); w.Code != 200 {
		t.Fatalf("install %d %s", w.Code, w.Body.String())
	}
	if w := action("enable", plugin.CommentOrigin, p.CSRFToken); w.Code != 200 {
		t.Fatalf("enable %d %s", w.Code, w.Body.String())
	}
	for _, route := range []string{"/admin/plugins/", "/admin/plugins/comment-origin/"} {
		w := serveCommentRequest(h, cookie, http.MethodGet, "https://example.test"+route, "", "", "8.8.8.8:80")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "评论来源") {
			t.Fatalf("page %s status=%d %s", route, w.Code, w.Body.String())
		}
	}
	c, err := comments.Create(t.Context(), comment.CreateInput{PostID: 1, AuthorName: "Visitor", AuthorEmail: "visitor@example.test", ContentSource: "来源展示", Status: comment.StatusApproved})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "https://example.test/", nil)
	r.RemoteAddr = "8.8.8.8:4400"
	r.Header.Set("X-Forwarded-For", "1.1.1.1")
	r.Header.Set("User-Agent", "Windows NT 10.0 Chrome/135 Safari/537 Edg/135")
	s.captureCommentOrigin(r, c.ID)
	record, err := s.commentOrigin.PrivateRecord(c.ID)
	if err != nil || record.IP != "8.8.8.8" {
		t.Fatalf("untrusted XFF captured: %+v %v", record, err)
	}
	response := s.publicCommentWithOrigin(r, c)
	raw, _ := json.Marshal(response)
	if !strings.Contains(string(raw), "Windows") || strings.Contains(string(raw), "8.8.8.8") || strings.Contains(string(raw), "Edg/135") {
		t.Fatalf("projection=%s", raw)
	}
	page := serveCommentRequest(h, nil, http.MethodGet, "https://example.test/posts/welcome-to-newpress", "", "", "1.1.1.1:80")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "comment-origin-badges") || !strings.Contains(page.Body.String(), "Windows · Edge") || strings.Contains(page.Body.String(), "8.8.8.8") {
		t.Fatalf("public HTML origin %d", page.Code)
	}
	settings, _ := s.commentOrigin.Settings()
	settings.Rules[0].Device = false
	settings.Rules[0].Location = false
	if _, err = s.commentOrigin.Save(settings, false); err != nil {
		t.Fatal(err)
	}
	if v := s.publicCommentWithOrigin(r, c); v.Origin.Device != "" {
		t.Fatal("author role suppression ignored")
	}
	if w := action("uninstall", plugin.CommentOrigin, p.CSRFToken); w.Code != 200 {
		t.Fatalf("uninstall %d %s", w.Code, w.Body.String())
	}
	if _, err = s.commentOrigin.PrivateRecord(c.ID); err == nil {
		t.Fatal("private record survived uninstall")
	}
	if _, err = comments.Get(t.Context(), c.ID); err != nil {
		t.Fatal("uninstall damaged comment")
	}
	if v := s.publicCommentWithOrigin(r, c); v.Origin.Device != "" {
		t.Fatal("uninstalled plugin displayed")
	}
}

func TestPluginSettingsSecretsStayOnServerAndConflictPreservesConfig(t *testing.T) {
	s, h, _, cookie, p := newCommentHTTPTestServer(t)
	if err := s.plugins.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	settings, _ := s.commentOrigin.Settings()
	settings.Provider = "baidu"
	settings.Key = "api-secret-sentinel"
	body, _ := json.Marshal(map[string]any{"settings": settings, "expected_user_id": p.UserID})
	write := func() *httptest.ResponseRecorder {
		return serveAdminCommentAPI(h, cookie, p.CSRFToken, http.MethodPut, "https://example.test/api/v1/admin/plugins/comment-origin/settings", string(body), "", "https://example.test")
	}
	w := write()
	if w.Code != 200 || strings.Contains(w.Body.String(), settings.Key) {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	if w = write(); w.Code != 409 {
		t.Fatalf("stale config %d %s", w.Code, w.Body.String())
	}
	w = serveCommentRequest(h, cookie, http.MethodGet, "https://example.test/admin/plugins/comment-origin/", "", "", "8.8.8.8:80")
	if w.Code != 200 || strings.Contains(w.Body.String(), settings.Key) {
		t.Fatalf("secret reflected %d %s", w.Code, w.Body.String())
	}
	stored, _ := s.commentOrigin.Settings()
	if stored.Key != settings.Key {
		t.Fatal("conflict erased key")
	}
}

func TestCommentOriginSubmissionRetryPreservesInitialSource(t *testing.T) {
	s, h, _, _, _ := newCommentHTTPTestServer(t)
	if err := s.plugins.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	if err := s.plugins.SetEnabled(plugin.CommentOrigin, true); err != nil {
		t.Fatal(err)
	}
	snap := s.siteSettings.Snapshot()
	snap.Settings.Discussion.RequireLogin = false
	snap.Settings.Discussion.RequireModeration = false
	if _, fields, err := s.siteSettings.Save(snap.Revision, snap.Settings); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	post, err := s.repository.GetPublishedBySlug(t.Context(), "welcome-to-newpress")
	if err != nil {
		t.Fatal(err)
	}
	payload := publicCommentCreateAPIRequest{FormToken: s.commentGuard.formToken(post.ID, time.Now()), AuthorName: "初次提交", Email: "retry@example.test", Content: "记录首次提交的来源。"}
	b, _ := json.Marshal(payload)
	send := func(ip string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/posts/welcome-to-newpress/comments", strings.NewReader(string(b)))
		r.RemoteAddr = ip + ":5000"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://example.test")
		r.Header.Set("Idempotency-Key", "origin-stable-retry")
		r.Header.Set("User-Agent", "Windows NT 10.0 Chrome/131")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := send("8.8.8.8")
	if first.Code != http.StatusCreated {
		t.Fatalf("first %d %s", first.Code, first.Body.String())
	}
	var result publicCommentCreateAPIResponse
	if err = json.Unmarshal(first.Body.Bytes(), &result); err != nil || result.Comment == nil {
		t.Fatal(err, first.Body.String())
	}
	replay := send("1.1.1.1")
	if replay.Code != http.StatusOK {
		t.Fatalf("replay %d %s", replay.Code, replay.Body.String())
	}
	record, err := s.commentOrigin.PrivateRecord(result.Comment.ID)
	if err != nil || record.IP != "8.8.8.8" {
		t.Fatalf("retry replaced original IP %+v %v", record, err)
	}
	for _, w := range []*httptest.ResponseRecorder{first, replay} {
		if strings.Contains(w.Body.String(), "8.8.8.8") || strings.Contains(w.Body.String(), "Chrome/131") {
			t.Fatal("raw source leaked")
		}
	}
}
