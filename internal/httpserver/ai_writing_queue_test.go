package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
)

func enableWritingFixture(t *testing.T, s *Server) {
	t.Helper()
	if err := s.plugins.InstallBundled(plugin.AIProMax); err != nil {
		t.Fatal(err)
	}
	if err := s.plugins.SetEnabled(plugin.AIProMax, true); err != nil {
		t.Fatal(err)
	}
	_, err := s.aiSettings.Save(aieditor.Settings{Revision: s.aiSettings.Snapshot().Revision, DefaultProvider: "writer", Providers: []aieditor.Provider{{ID: "writer", Name: "测试文本接口", BaseURL: "https://writer.example.test/v1", APIKey: "fixture-only-key", Models: []string{"writer-model"}, DefaultModel: "writer-model"}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAIProMaxTopicSuggestionsUsePublicTitlesAndWaitForSelection(t *testing.T) {
	s, handler, _, cookie, actor := newCommentHTTPTestServer(t)
	enableWritingFixture(t, s)
	for _, item := range []struct{ status, title, slug string }{{content.StatusPublish, "选题素材公开标题", "topic-public"}, {content.StatusDraft, "不能发送的私有标题", "topic-private"}} {
		_, err := s.repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: item.status, Title: item.title, Slug: item.slug, ContentSource: "不能发送的文章正文", AuthorName: "fixture", Actor: actor.Username})
		if err != nil {
			t.Fatal(err)
		}
	}
	var calls int
	setAIResponse(s, 200, "text/event-stream", aiFrame("```json\n[\"编辑怎样归档原始材料\",\"编辑怎样归档原始材料\",\"站点索引如何维护\"]\n```", "stop")+"data: [DONE]\n\n", func(r *http.Request) {
		calls++
		var body struct {
			Messages []aieditor.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 {
			t.Fatal("missing topic prompt")
		}
		ctx := body.Messages[1].Content
		if !strings.Contains(ctx, "选题素材公开标题") || strings.Contains(ctx, "不能发送") {
			t.Fatal("unexpected topic context", ctx)
		}
	})
	const endpoint = "https://example.test/api/v1/admin/plugins/ai-pro-max/topics"
	body := `{"model":"writer:writer-model","target":"post","language":"zh-CN"}`
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": actor.CSRFToken}
	if r := serveContentAPI(handler, cookie, "POST", endpoint, strings.Replace(body, `"post"`, `"category:999999"`, 1), headers); r.Code != 422 || calls != 0 {
		t.Fatal("missing category invoked model", r.Code)
	}
	r := serveContentAPI(handler, cookie, "POST", endpoint, body, headers)
	var payload struct {
		Topics []string `json:"topics"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &payload); err != nil || r.Code != 200 || len(payload.Topics) != 2 || calls != 1 {
		t.Fatal(r.Code, r.Body.String(), err)
	}
	snapshot, err := s.aiJobs.Snapshot()
	if err != nil || len(snapshot.Jobs) > 0 {
		t.Fatal("suggestion implicitly scheduled writing", snapshot, err)
	}
	if err := s.plugins.SetEnabled(plugin.AIProMax, false); err != nil {
		t.Fatal(err)
	}
	if r := serveContentAPI(handler, cookie, "POST", endpoint, body, headers); r.Code != 404 || calls != 1 {
		t.Fatal("inactive plugin invoked model", r.Code)
	}
}

func TestAIProMaxScheduledArticleEntersEditorOnly(t *testing.T) {
	s, h, manager, repo, cookie, p := newPersistentAdminTestServer(t)
	enableWritingFixture(t, s)
	snap := s.siteSettings.Snapshot()
	snap.Settings.Writing.DefaultStatus = content.StatusPending
	if _, fields, err := s.siteSettings.Save(snap.Revision, snap.Settings); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	const base = "https://example.test"
	const path = base + "/admin/plugins/ai-pro-max/queue/"
	page := serveCookieRequest(h, "GET", base+"/admin/plugins/ai-pro-max/", cookie, nil)
	if page.Code != 200 {
		t.Fatal(page.Code)
	}
	field := func(name string) string {
		t.Helper()
		m := regexp.MustCompile(`name="` + name + `" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
		if len(m) != 2 {
			t.Fatal("missing", name)
		}
		return m[1]
	}
	categories, err := s.aiEditorCategories(t.Context())
	if err != nil || len(categories) == 0 {
		t.Fatal(categories, err)
	}
	category := strconv.FormatInt(categories[0].ID, 10)
	f := url.Values{"action": {"plan"}, "key": {field("key")}, "epoch": {field("epoch")}, "csrf_token": {p.CSRFToken}, "topics": {"怎样安排选题会"}, "model": {"writer:writer-model"}, "target": {"category:" + category}, "start": {""}, "language": {"zh-CN"}, "interval": {"0"}}
	post := func(c *http.Cookie, origin string) *httptest.ResponseRecorder {
		return serveContentAPI(h, c, "POST", path, f.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": origin})
	}
	if r := post(cookie, ""); r.Code != 403 {
		t.Fatal("missing origin", r.Code)
	}
	f.Set("csrf_token", "invalid")
	if r := post(cookie, base); r.Code != 403 {
		t.Fatal("bad CSRF", r.Code)
	}
	_, editor, ep := createAndLoginTestUser(t, manager, p, "writing-editor", "writing-editor@example.test", "编辑", "editor")
	f.Set("csrf_token", ep.CSRFToken)
	if r := post(editor, base); r.Code != 403 {
		t.Fatal("editor altered plugin queue", r.Code)
	}
	f.Set("csrf_token", p.CSRFToken)
	for range 2 {
		if r := post(cookie, base); r.Code != 303 || r.Header().Get("Location") != aiQueueHome {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	st, err := s.aiJobs.Snapshot()
	if err != nil || len(st.Jobs) != 1 {
		t.Fatal(st, err)
	}
	j := st.Jobs[0]
	draft := path + j.ID + "/draft/"
	if r := serveCookieRequest(h, "GET", draft, cookie, nil); r.Code != 409 {
		t.Fatal("premature draft", r.Code)
	}
	var calls atomic.Int32
	setAIResponse(s, 200, "text/event-stream", aiFrame(`<newpress-meta>{"title":"选题会纪要","slug":"weekly-topics","tags":["选题"]}</newpress-meta>`+"\n\n一份供编辑核对的正文。", "stop")+"data: [DONE]\n\n", func(*http.Request) { calls.Add(1) })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.RunAIWritingQueue(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(4 * time.Second)
	for {
		st, err = s.aiJobs.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if st.Jobs[0].Terminal() || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.Jobs[0].Status != "succeeded" || calls.Load() != 1 {
		t.Fatal(st.Jobs, calls.Load())
	}
	if r := serveCookieRequest(h, "GET", draft, editor, nil); r.Code != 403 {
		t.Fatal("unauthorized draft read", r.Code)
	}
	r := serveCookieRequest(h, "GET", draft, cookie, nil)
	for _, m := range []string{"选题会纪要", "一份供编辑核对的正文", `value="draft" selected`, `data-editor-force-dirty="true"`, `action="/admin/content/new/"`, "生成结果尚未保存"} {
		if r.Code != 200 || !strings.Contains(r.Body.String(), m) {
			t.Fatal("editor missing", m, r.Code)
		}
	}
	list, err := repo.ListAdmin(t.Context(), content.AdminQuery{Limit: 20})
	if err != nil || list.Total != 0 {
		t.Fatal("viewing or generating saved a post", list.Total, err)
	}
	if err := s.plugins.SetEnabled(plugin.AIProMax, false); err != nil {
		t.Fatal(err)
	}
	if r := serveCookieRequest(h, "GET", draft, cookie, nil); r.Code != 404 {
		t.Fatal("inactive plugin exposed draft", r.Code)
	}
}

func TestAIProMaxWorkerRechecksPersistentActorAndModel(t *testing.T) {
	s, _, m, _, _, owner := newPersistentAdminTestServer(t)
	enableWritingFixture(t, s)
	user, _, p := createAndLoginTestUser(t, m, owner, "queue-manager", "queue-manager@example.test", "排程管理员", "administrator")
	j := aijobs.Job{Author: p.Username, UserID: user.ID, Provider: "writer", Model: "writer-model", Language: "zh-CN", PostType: "post", Topic: "资料编目"}
	setAIResponse(s, 200, "text/event-stream", "", func(*http.Request) { t.Fatal("invalid task called provider") })
	if !s.writingActorAllowed(t.Context(), j) {
		t.Fatal("active administrator rejected")
	}
	j.Model = "removed-model"
	if _, err := s.generateAIWritingJob(t.Context(), j); !errors.Is(err, aijobs.ErrModel) {
		t.Fatal(err)
	}
	_, err := m.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: user.Status, RoleSlugs: []string{"editor"}})
	if err != nil {
		t.Fatal(err)
	}
	j.Model = "writer-model"
	if _, err = s.generateAIWritingJob(t.Context(), j); !errors.Is(err, aijobs.ErrActor) {
		t.Fatal("revoked role still allowed", err)
	}
	j.UserID = 0
	j.Author = m.Username()
	if s.writingActorAllowed(t.Context(), j) {
		t.Fatal("persistent auth accepted environment-only identity")
	}
}
