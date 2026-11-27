package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/feedback"
	"newpress.local/newpress/internal/wechat"
)

func featureStatusRead(t *testing.T, h http.Handler, cookie *http.Cookie, feature string) adminFeatureStatus {
	t.Helper()
	r := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/feature-status?feature="+feature, "", nil)
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %s: %d %s", feature, r.Code, r.Body.String())
	}
	var result adminFeatureStatus
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFeatureStatusCompactOverviewKeepsOrderedReasonsAndEditorScope(t *testing.T) {
	settings := &featureStatusAction{Label: "调整本篇评论", Allowed: true}
	v := adminFeatureStatus{Feature: "article-comments", Items: []featureStatusItem{
		{State: "paused", Label: "已暂停", Title: "本篇评论", Summary: "本篇关闭", Action: settings,
			Checks: []featureStatusCheck{{ID: "module", State: "ready"}, {ID: "post", State: "paused"}}},
	}}
	if o := summarizeFeatureStatus(v); o.GlobalBlock || o.Action != settings || o.Title != "本篇评论" {
		t.Fatalf("local pause must stay with publication settings: %+v", o)
	}
	v.Items[0].Checks[0].State = "paused"
	if !summarizeFeatureStatus(v).GlobalBlock {
		t.Fatal("site-wide pause must keep the editor reminder")
	}
	v.Feature = "progression"
	v.Items = append([]featureStatusItem{{State: "ready"}}, v.Items...)
	v.Items = append(v.Items, featureStatusItem{State: "partial", Label: "部分可用", Effective: true})
	if o := summarizeFeatureStatus(v); o.Additional != 1 || o.Action != settings || o.GlobalBlock {
		t.Fatalf("summary must preserve first reason and count partially usable features: %+v", o)
	}
	v.Items = []featureStatusItem{{State: "ready"}, {State: "ready"}}
	if o := summarizeFeatureStatus(v); o.Label != "功能可用" || o.Summary != "" || o.Action != nil || o.Additional != 0 {
		t.Fatalf("ready overview should be a single quiet label: %+v", o)
	}
}

func TestFeatureStatusAICollectionIndependentOfVisitsAndReports(t *testing.T) {
	s, h, _, _, cookie, _ := newPersistentAdminTestServer(t)
	s.config.AnalyticsEnabled = false
	v := featureStatusRead(t, h, cookie, "ai-usage")
	if len(v.Items) != 2 || !v.Items[0].Effective || !v.Items[1].Effective {
		t.Fatalf("visit collection must not block AI reports or collection: %+v", v)
	}
	settings := s.aiSettings.Snapshot()
	cfg := aieditor.DefaultTelemetryConfig()
	cfg.Enabled, cfg.Charts = false, false
	settings.Telemetry = &cfg
	if _, err := s.aiSettings.Save(settings); err != nil {
		t.Fatal(err)
	}
	v = featureStatusRead(t, h, cookie, "ai-usage")
	if !v.Items[0].Effective || v.Items[1].State != "paused" {
		t.Fatal("disabling collection must retain report access")
	}
	snapshot := s.siteSettings.Snapshot()
	snapshot.Settings.Modules = map[string]bool{"analytics": false}
	if _, _, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	settings = s.aiSettings.Snapshot()
	settings.Telemetry = &cfg
	if _, err := s.aiSettings.Save(settings); err != nil {
		t.Fatal(err)
	}
	v = featureStatusRead(t, h, cookie, "ai-usage")
	if v.Items[0].State != "paused" || !v.Items[1].Effective {
		t.Fatal("report module must not masquerade as the AI collection switch")
	}
}

func TestFeatureStatusUnavailableServicesKeepAuthorizedDiagnostics(t *testing.T) {
	s, h, _, _, cookie, _ := newPersistentAdminTestServer(t)
	s.currency, s.currencyCatalog, s.progression, s.questions = nil, nil, nil, nil
	for _, feature := range []string{"currency", "progression", "questions"} {
		path := "https://example.test/admin/" + feature + "/"
		response := serveContentAPI(h, cookie, "GET", path, "", nil)
		if response.Code != 503 || !strings.Contains(response.Body.String(), "data-feature-status") {
			t.Fatalf("%s lost its diagnostic surface: %d", feature, response.Code)
		}
		if result := serveContentAPI(h, nil, "GET", path, "", nil); result.Code != 303 {
			t.Fatalf("%s diagnostic bypassed authentication: %d", feature, result.Code)
		}
		v := featureStatusRead(t, h, cookie, feature)
		if v.Items[0].State != "error" {
			t.Fatalf("%s missing service marked ready", feature)
		}
	}
}

func TestFeatureStatusAllPrerequisitesAndNoSecrets(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	initial := s.siteSettings.Snapshot()
	settings := initial.Settings
	settings.Modules = map[string]bool{"ai": false, "wechat": false, "community": false}
	if _, fields, err := s.siteSettings.Save(initial.Revision, settings); err != nil || len(fields) != 0 {
		t.Fatal(err, fields)
	}
	v := featureStatusRead(t, h, cookie, "ai-queue")
	if r := serveContentAPI(h, cookie, "GET", "https://example.test/admin/plugins/ai-pro-max/", "", nil); r.Code != 404 || !strings.Contains(r.Body.String(), "data-feature-status") {
		t.Fatal("missing plugin lost its configuration explanation", r.Code)
	}
	if len(v.Items) != 1 || v.Items[0].State != "paused" || v.Items[0].Effective {
		t.Fatalf("bad queue state: %+v", v)
	}
	blocked := map[string]bool{}
	for _, c := range v.Items[0].Checks {
		if c.State != "ready" && c.State != "info" {
			blocked[c.ID] = true
		}
	}
	for _, key := range []string{"module", "plugin", "models"} {
		if !blocked[key] {
			t.Fatalf("missing condition %s: %+v", key, v)
		}
	}
	if v.Items[0].Action == nil || !v.Items[0].Action.Allowed || v.Items[0].Action.Module != "ai" {
		t.Fatal("missing authorized recovery")
	}
	community := featureStatusRead(t, h, cookie, "community")
	if len(community.Items[0].Checks) < 2 {
		t.Fatal("paused module concealed server prerequisite")
	}
	cfg, err := wechat.ReadConfig(t.Context(), s.wechat.Store)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled, cfg.AppID, cfg.Secret = true, "wx0123456789abcdef", "status-private-secret-0123456789"
	if _, err := s.wechat.SaveConfig(t.Context(), cfg, false); err != nil {
		t.Fatal(err)
	}
	wechatStatus := featureStatusRead(t, h, cookie, "wechat")
	b, _ := json.Marshal(wechatStatus)
	if strings.Contains(string(b), cfg.Secret) || strings.Contains(string(b), cfg.AppID) {
		t.Fatal("status exposed credentials")
	}
	if len(wechatStatus.Items) != 2 || wechatStatus.UserID != owner.UserID {
		t.Fatal("missing capability split")
	}
	if s.siteSettings.Snapshot().Revision != v.Revision {
		t.Fatal("reading state changed settings")
	}
}

func TestFeatureStatusRecoveryAndRevisionConflict(t *testing.T) {
	s, h, users, _, cookie, owner := newPersistentAdminTestServer(t)
	initial := s.siteSettings.Snapshot()
	cfg := initial.Settings
	cfg.Modules = map[string]bool{"comments": false}
	if _, _, err := s.siteSettings.Save(initial.Revision, cfg); err != nil {
		t.Fatal(err)
	}
	v := featureStatusRead(t, h, cookie, "comments")
	r := serveContentAPI(h, cookie, "GET", "https://example.test/admin/comments/", "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "data-feature-module=\"comments\"") || strings.Contains(r.Body.String(), "data-comment-admin") {
		t.Fatal("recovery page missing or business ran", r.Code)
	}
	if r := serveContentAPI(h, nil, "GET", "https://example.test/admin/comments/", "", nil); r.Code != 303 {
		t.Fatal("recovery page was not authenticated", r.Code)
	}
	_, authorCookie, _ := createAndLoginTestUser(t, users, owner, "status-author", "status-author@example.test", "作者", "author")
	if r := serveContentAPI(h, authorCookie, "GET", "https://example.test/admin/comments/", "", nil); r.Code != 403 {
		t.Fatal("recovery page bypassed permission", r.Code)
	}
	if r := serveContentAPI(h, cookie, "POST", "https://example.test/admin/comments/", "", nil); r.Code != 404 {
		t.Fatal("paused mutation reached handler", r.Code)
	}
	if r := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/comments", "", nil); r.Code != 404 {
		t.Fatal("paused API exposed records")
	}
	body := fmt.Sprintf(`{"revision":%q,"modules":{"comments":true}}`, initial.Revision)
	if r := serveContentAPI(h, cookie, "PATCH", "https://example.test/api/v1/admin/modules", body, authenticatedAPIHeaders(owner)); r.Code != 409 {
		t.Fatal("stale action accepted", r.Code)
	}
	body = fmt.Sprintf(`{"revision":%q,"modules":{"comments":true}}`, v.Revision)
	if r := serveContentAPI(h, cookie, "PATCH", "https://example.test/api/v1/admin/modules", body, authenticatedAPIHeaders(owner)); r.Code != 200 {
		t.Fatal("recovery failed", r.Code)
	}
	if !featureStatusRead(t, h, cookie, "comments").Items[0].Effective {
		t.Fatal("successful save did not update readiness")
	}
}

func TestFeatureStatusPermissionsAndSavedArticle(t *testing.T) {
	s, h, users, repo, cookie, owner := newPersistentAdminTestServer(t)
	_, authorCookie, author := createAndLoginTestUser(t, users, owner, "status-reader", "status-reader@example.test", "作者", "author")
	for _, path := range []string{"wechat", "recognition", "ai-queue", "features"} {
		if r := serveContentAPI(h, authorCookie, "GET", "https://example.test/api/v1/admin/feature-status?feature="+path, "", nil); r.Code != 403 {
			t.Fatalf("status permission %s: %d", path, r.Code)
		}
	}
	if r := serveContentAPI(h, nil, "GET", "https://example.test/api/v1/admin/feature-status?feature=wechat", "", nil); r.Code != 401 {
		t.Fatal("unauthenticated state", r.Code)
	}
	post := createOwnedTestPost(t, repo, owner.UserID, owner.DisplayName, "status-owned", "不可泄露的文章")
	if r := serveContentAPI(h, authorCookie, "GET", fmt.Sprintf("https://example.test/api/v1/admin/feature-status?feature=article-comments&object=%d", post.ID), "", nil); r.Code != 404 {
		t.Fatal("other author's object was disclosed", r.Code)
	}
	for _, feature := range []string{"invalid", "comments&object=1", "currency&object=" + strings.Repeat("x", 90)} {
		if r := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/feature-status?feature="+feature, "", nil); r.Code != 400 {
			t.Fatal("invalid status query accepted", r.Code)
		}
	}
	owned := createOwnedTestPost(t, repo, author.UserID, author.DisplayName, "status-author-owned", "作者文章")
	state := featureStatusRead(t, h, authorCookie, fmt.Sprintf("article-comments&object=%d", owned.ID))
	if !state.Items[0].Effective {
		t.Fatal("saved open article incorrectly blocked")
	}
	input := content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Slug: owned.Slug, Title: owned.Title, ContentSource: owned.ContentSource, AuthorID: author.UserID, AuthorName: author.DisplayName, CommentStatus: content.CommentClosed}
	if _, err := repo.Update(t.Context(), owned.ID, content.Mutation{ExpectedVersion: owned.Version, Actor: author.Username}, input); err != nil {
		t.Fatal(err)
	}
	state = featureStatusRead(t, h, authorCookie, fmt.Sprintf("article-comments&object=%d", owned.ID))
	if state.Items[0].State != "paused" || !s.moduleEnabled("comments") {
		t.Fatal("per-article state conflated with global state")
	}
}

func TestFeatureStatusIndependentModesAndRewardSources(t *testing.T) {
	s, h, _, _, cookie, _ := newPersistentAdminTestServer(t)
	v := featureStatusRead(t, h, cookie, "wechat")
	if v.Items[0].Effective || !v.Items[1].Effective {
		t.Fatalf("sync and collection conflated: %+v", v)
	}
	cfg, _ := wechat.ReadConfig(t.Context(), s.wechat.Store)
	cfg.Enabled, cfg.AppID, cfg.Secret = true, "wx0123456789abcdef", "status-private-secret-0123456789"
	if _, err := s.wechat.SaveConfig(t.Context(), cfg, false); err != nil {
		t.Fatal(err)
	}
	v = featureStatusRead(t, h, cookie, "wechat")
	if !v.Items[0].Effective || !strings.Contains(v.Items[0].Mode, "仅手动") {
		t.Fatal("manual mode treated as blocked")
	}
	store, settings, coin := progressionHTTPStore(t, s)
	wallet, err := coin.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wallet.CheckInEnabled = true
	if _, err := coin.UpdateSettings(t.Context(), wallet.Version, wallet); err != nil {
		t.Fatal(err)
	}
	chosen, _, err := s.resolveHomeCheckIn(t.Context())
	if err != nil || chosen.Source != "progression" {
		t.Fatal("growth priority lost", err)
	}
	status := featureStatusRead(t, h, cookie, "progression")
	if status.Items[len(status.Items)-1].Mode != "成长签到" {
		t.Fatal("home explanation differs from rendering")
	}
	settings.Experience.Enabled = false
	if _, err := store.UpdateSettings(t.Context(), settings.Version, settings); err != nil {
		t.Fatal(err)
	}
	status = featureStatusRead(t, h, cookie, "progression")
	if status.Items[0].Effective || status.Items[0].Checks[0].State != "paused" {
		t.Fatal("missing experience dependency")
	}
	chosen, _, _ = s.resolveHomeCheckIn(t.Context())
	if chosen.Source != "wallet" {
		t.Fatal("wallet fallback lost")
	}
	if status.Items[len(status.Items)-1].Mode != "主钱包签到" {
		t.Fatal("fallback explanation differs from rendering")
	}
}

func TestFeatureStatusPageFallbackAndLocalPause(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	cfg := feedback.Defaults()
	cfg.Name, cfg.Enabled = "状态测试板块", false
	if _, err := s.feedback.Apply(t.Context(), "status", feedbackActor(owner), feedback.Command{Action: "create", Key: "feature-status-board", Settings: &cfg}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	v := featureStatusRead(t, h, cookie, "feedback&object=status")
	if v.Items[0].Effective || v.Items[0].State != "paused" || v.Items[0].Title != cfg.Name {
		t.Fatal("page pause conflated with template availability")
	}
	readOnly := adminView{Principal: auth.Principal{}, ReadOnly: true, Preview: true}
	initial := s.siteSettings.Snapshot()
	initial.Settings.Modules = map[string]bool{"feedback": false}
	if _, _, err := s.siteSettings.Save(initial.Revision, initial.Settings); err != nil {
		t.Fatal(err)
	}
	v = s.featureStatus(t.Context(), "feedback", "status", readOnly)
	if v.Items[0].Action == nil || v.Items[0].Action.Allowed {
		t.Fatal("preview offered a write")
	}
	r := serveContentAPI(h, cookie, "GET", "https://example.test/admin/features/?edit=status", "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "data-feature-status") {
		t.Fatal("paused board has no in-context recovery", r.Code)
	}
}
