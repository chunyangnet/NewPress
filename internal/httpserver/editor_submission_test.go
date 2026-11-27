package httpserver

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func TestEditorSubmissionReplayPrecedesTimeSensitiveValidation(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	form := url.Values{"csrf_token": {principal.CSRFToken}, "action": {"save"}, "title": {"已完成的定时保存"}, "status": {"future"}, "type": {"post"}, "published_at": {"2020-01-01T12:00"}, "comment_status": {"open"}}
	values := cloneValues(form)
	values.Del("csrf_token")
	owner, _ := autosaveOwnerKey(principal)
	request := content.Submission{
		Key:         fmt.Sprintf("%x", sha256.Sum256([]byte(owner+"\x00editor\x00/admin/content/new/\x00editor-old-schedule"))),
		Fingerprint: fmt.Sprintf("%x", sha256.Sum256([]byte(values.Encode()))), Action: "create", Input: content.PostInput{Title: "原请求已成功", Status: content.StatusPublish},
	}
	created, err := repository.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	w := editorSubmit(handler, cookie, "/admin/content/new/", "editor-old-schedule", form, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) || !strings.Contains(w.Body.String(), fmt.Sprintf(`"id":%d`, created.Receipt.PostID)) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestEditorSubmissionReauthenticationAndAuthorScope(t *testing.T) {
	_, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
	user, cookie, principal := createAndLoginTestUser(t, manager, owner, "save-author", "save-author@example.test", "作者", "author")
	form := url.Values{"csrf_token": {principal.CSRFToken}, "action": {"save"}, "title": {"重登恢复"}, "type": {"post"}, "status": {"draft"}, "comment_status": {"open"}, "published_timezone": {"Asia/Shanghai"}, "published_source": {"canonical"}}
	first := editorSubmit(handler, cookie, "/admin/content/new/", "editor-owner-key", form, nil)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	var result struct{ ID int64 }
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	token, renewed, err := manager.AuthenticateContext(t.Context(), "save-author-new-session", "save-author", "Account password for save-author 2026")
	if err != nil {
		t.Fatal(err)
	}
	newCookie := &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}
	form.Set("csrf_token", renewed.CSRFToken)
	replayed := editorSubmit(handler, newCookie, "/admin/content/new/", "editor-owner-key", form, nil)
	if replayed.Code != 200 || !strings.Contains(replayed.Body.String(), `"replayed":true`) {
		t.Fatal(replayed.Code, replayed.Body.String())
	}
	post, _ := repository.GetByID(t.Context(), result.ID)
	if post.AuthorID != user.ID || post.Version != 1 {
		t.Fatal(post)
	}
	_, otherCookie, other := createAndLoginTestUser(t, manager, owner, "save-other", "save-other@example.test", "其他作者", "author")
	form.Set("csrf_token", other.CSRFToken)
	form.Set("version", "1")
	path := "/admin/content/" + strconv.FormatInt(post.ID, 10) + "/edit/"
	if w := editorSubmit(handler, otherCookie, path, "editor-owner-key", form, nil); w.Code != http.StatusForbidden {
		t.Fatal(w.Code, w.Body.String())
	}
	form.Del("version")
	form.Set("slug", "other-author-save")
	if w := editorSubmit(handler, otherCookie, "/admin/content/new/", "editor-owner-key", form, nil); w.Code != 200 || strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	_, subscriberCookie, subscriber := createAndLoginTestUser(t, manager, owner, "save-reader", "save-reader@example.test", "读者", "subscriber")
	form.Set("csrf_token", subscriber.CSRFToken)
	if w := editorSubmit(handler, subscriberCookie, "/admin/content/new/", "editor-owner-key", form, nil); w.Code != http.StatusForbidden {
		t.Fatal(w.Code, w.Body.String())
	}
}

func editorSubmit(handler http.Handler, cookie *http.Cookie, path, key string, form url.Values, change func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "https://example.test"+path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://example.test")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-NewPress-Editor", "1")
	r.Header.Set("Idempotency-Key", key)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if change != nil {
		change(r)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestEditorSubmissionCreateReplayAndConflict(t *testing.T) {
	s, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	settings := s.siteSettings.Snapshot()
	settings.Settings.API.AdminContentEnabled = false
	if _, _, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {principal.CSRFToken}, "action": {"save"}, "type": {"post"}, "status": {"draft"}, "title": {"首次草稿"}, "content_source": {"<p>原文</p>"}, "comment_status": {"open"}, "published_timezone": {"Asia/Shanghai"}, "published_source": {"canonical"}}
	var id int64
	for attempt := range 2 {
		w := editorSubmit(handler, cookie, "/admin/content/new/", "editor-create-one", form, nil)
		var result struct {
			Saved, Replayed bool
			ID, Version     int64
			SubmissionKey   string `json:"submission_key"`
			EditorURL       string `json:"editor_url"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err, w.Body.String())
		}
		if w.Code != 200 || !result.Saved || result.Replayed != (attempt == 1) || result.Version != 1 || result.SubmissionKey != "editor-create-one" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal(w.Code, w.Body.String())
		}
		id = result.ID
	}
	list, _ := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 20})
	if list.Total != 1 {
		t.Fatal(list)
	}
	changed := cloneValues(form)
	changed.Set("title", "不同内容")
	if w := editorSubmit(handler, cookie, "/admin/content/new/", "editor-create-one", changed, nil); w.Code != 409 || !strings.Contains(w.Body.String(), "submission_conflict") {
		t.Fatal(w.Code, w.Body.String())
	}
	path := "/admin/content/" + strconv.FormatInt(id, 10) + "/edit/"
	form.Set("version", "1")
	form.Set("title", "第二版")
	if w := editorSubmit(handler, cookie, path, "editor-save-one", form, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := repository.ChangeStatus(t.Context(), id, content.Mutation{ExpectedVersion: 2}, content.StatusPublish); err != nil {
		t.Fatal(err)
	}
	w := editorSubmit(handler, cookie, path, "editor-save-one", form, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":2`) || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := editorSubmit(handler, cookie, path, "editor-stale-version", form, nil); w.Code != 409 || !strings.Contains(w.Body.String(), "version_conflict") {
		t.Fatal(w.Code, w.Body.String())
	}
	revisions, _ := repository.ListRevisions(t.Context(), id, 20)
	if len(revisions) != 3 {
		t.Fatal(revisions)
	}
}

func TestEditorSubmissionUsesFormSecurityAndValidation(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	valid := url.Values{"csrf_token": {principal.CSRFToken}, "action": {"save"}, "title": {"安全保存"}, "type": {"post"}, "status": {"draft"}, "comment_status": {"open"}, "published_timezone": {"Asia/Shanghai"}, "published_source": {"canonical"}}
	for name, change := range map[string]func(*http.Request){
		"anonymous":        func(r *http.Request) { r.Header.Del("Cookie") },
		"origin":           func(r *http.Request) { r.Header.Set("Origin", "https://attacker.test") },
		"header":           func(r *http.Request) { r.Header.Set("X-NewPress-Editor", "2") },
		"empty_header":     func(r *http.Request) { r.Header.Set("X-NewPress-Editor", "") },
		"duplicate_header": func(r *http.Request) { r.Header.Add("X-NewPress-Editor", "1") },
		"no_key":           func(r *http.Request) { r.Header.Del("Idempotency-Key") },
		"duplicate_key":    func(r *http.Request) { r.Header.Add("Idempotency-Key", "editor-other") },
	} {
		t.Run(name, func(t *testing.T) {
			w := editorSubmit(handler, cookie, "/admin/content/new/", "editor-security", valid, change)
			if w.Code == 200 || w.Code == 303 && !strings.Contains(w.Header().Get("Location"), "login") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	for _, mutation := range []func(url.Values){func(f url.Values) { f.Set("csrf_token", "invalid") }, func(f url.Values) { f.Add("title", "duplicate") }, func(f url.Values) { f.Set("action", "publish") }, func(f url.Values) { f.Set("title", "") }} {
		form := cloneValues(valid)
		mutation(form)
		w := editorSubmit(handler, cookie, "/admin/content/new/", "editor-security", form, nil)
		if w.Code < 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	list, _ := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 20})
	if list.Total != 0 {
		t.Fatal("rejected writes persisted", list)
	}
	if w := editorSubmit(handler, cookie, "/admin/content/new/", "editor-security", valid, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestProAuthorPublishesReviewedFieldsWithOwnProvider(t *testing.T) {
	s, h, manager, repo, _, owner := newPersistentAdminTestServer(t)
	enableWritingFixture(t, s)
	user, cookie, actor := createAndLoginTestUser(t, manager, owner, "pro-author", "pro-author@example.test", "作者", "author")
	// This server has no wallet or membership service configured.
	const summary = "作者校对后的模型摘要"
	called := false
	setAIResponse(s, 200, "text/event-stream", aiFrame(`{"excerpt":"模型摘要"}`, "stop")+"data: [DONE]\n\n", func(r *http.Request) {
		called = true
		if r.URL.Host != "writer.example.test" || r.Header.Get("Authorization") != "Bearer fixture-only-key" {
			t.Error("editor did not use the configured provider")
		}
	})
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": actor.CSRFToken}
	w := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/plugins/ai-pro-max/metadata", `{"title":"原稿","body":"内容","language":"auto","fields":["excerpt"]}`, headers)
	if w.Code != 200 || !called || !strings.Contains(w.Body.String(), "模型摘要") {
		t.Fatal(w.Code, w.Body.String())
	}
	post := createOwnedTestPost(t, repo, user.ID, user.Username, "pro-recovery", "草稿")
	st, _ := s.aiJobs.Snapshot()
	rule := s.defaultProRules(st.Rules, s.aiSettings.Snapshot())
	rule.UserID = owner.UserID
	rule.Author = owner.Username
	rule.AutoPost = true
	if err := s.aiJobs.SaveRules(st.Epoch, rule); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {actor.CSRFToken}, "version": {strconv.FormatInt(post.Version, 10)}, "action": {"publish"}, "ai_pro_max": {"off"}, "type": {"post"}, "status": {"draft"}, "title": {"已审文章"}, "excerpt": {summary}, "slug": {post.Slug}, "content_source": {"<p>已审正文</p>"}, "comment_status": {"open"}, "published_timezone": {s.siteLocation().String()}}
	path := fmt.Sprintf("/admin/content/%d/edit/", post.ID)
	for _, replay := range []bool{false, true} {
		resp := editorSubmit(h, cookie, path, "pro-publish-reviewed-fields", form, nil)
		var receipt struct {
			Action   string
			Replayed bool
		}
		if resp.Code != 200 || json.Unmarshal(resp.Body.Bytes(), &receipt) != nil || receipt.Action != "publish" || receipt.Replayed != replay {
			t.Fatal(resp.Code, resp.Body.String())
		}
	}
	saved, err := repo.GetByID(t.Context(), post.ID)
	if err != nil || saved.Status != "publish" || saved.Title != "已审文章" || saved.Excerpt != summary || saved.Meta["ai_pro_max"] != "off" || saved.Version != post.Version+1 {
		t.Fatalf("wrong published fields: %+v, %v", saved, err)
	}
	st, _ = s.aiJobs.Snapshot()
	if len(st.Jobs) != 0 {
		t.Fatal("opted-out article was queued")
	}
}
