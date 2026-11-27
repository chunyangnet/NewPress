package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"strconv"
	"strings"
	"testing"
	"time"
)

func waitForProJob(t *testing.T, s *Server, kind, status string) aijobs.Job {
	t.Helper()
	end := time.Now().Add(8 * time.Second)
	for time.Now().Before(end) {
		state, err := s.aiJobs.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range state.Jobs {
			if (j.Work.Kind == kind || kind == "") && j.Status == status {
				return j
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, _ := s.aiJobs.Snapshot()
	t.Fatalf("worker timeout: %+v", st.Jobs)
	return aijobs.Job{}
}

func TestProWorkflowRecoveryDoesNotRegenerateSavedArticle(t *testing.T) {
	s, _, _, _, actor := newCommentHTTPTestServer(t)
	enableWritingFixture(t, s)
	st, _ := s.aiJobs.Snapshot()
	j := aijobs.Job{Author: actor.Username, UserID: actor.UserID, Topic: "恢复计划", Work: aijobs.WorkSpec{Kind: "workflow", Key: "already-committed-workflow"}}
	key := aiDigest("workflow/" + j.Work.Key)
	saved, err := s.aiBaseContent().Submit(t.Context(), content.Submission{Key: key, Fingerprint: key, Action: "create", Actor: actor.Username, Input: content.PostInput{Title: "已经保存的文章", Slug: "already-saved-workflow", Status: content.StatusPending}})
	if err != nil {
		t.Fatal(err)
	}
	setAIResponse(s, 200, "text/event-stream", "", func(*http.Request) { t.Error("recovery called the model again") })
	if err := s.aiJobs.AddWork(st.Epoch, []aijobs.Job{j}, time.Now()); err != nil {
		t.Fatal(err)
	}
	startProWorker(t, s)
	done := waitForProJob(t, s, "workflow", "succeeded")
	if !strings.Contains(done.Result, "#"+strconv.FormatInt(saved.Receipt.PostID, 10)) {
		t.Fatal(done.Result)
	}
	p, err := s.repository.GetByID(t.Context(), saved.Receipt.PostID)
	if err != nil || p.Version != 1 || p.Title != "已经保存的文章" {
		t.Fatal(p, err)
	}
}

func TestProBridgeValidation(t *testing.T) {
	valid := func() bridgeDocument {
		return bridgeDocument{Site: "https://wordpress.example/blog", Type: "product", ID: 1, Title: "商品", Revision: strings.Repeat("a", 64), Fields: []string{"excerpt", "tags"}, Categories: []aieditor.Category{{ID: 1, Name: "商品"}}}
	}
	if !validBridgeDocument(valid()) {
		t.Fatal("valid WordPress document rejected")
	}
	for name, change := range map[string]func(*bridgeDocument){
		"invalid revision":    func(d *bridgeDocument) { d.Revision = strings.Repeat("z", 64) },
		"unknown field":       func(d *bridgeDocument) { d.Fields = []string{"body"} },
		"duplicate field":     func(d *bridgeDocument) { d.Fields = []string{"excerpt", "excerpt"} },
		"invalid type":        func(d *bridgeDocument) { d.Type = "" },
		"credentials in site": func(d *bridgeDocument) { d.Site = "https://user:secret@example.com" },
		"invalid site":        func(d *bridgeDocument) { d.Site = "javascript:alert(1)" },
		"invalid attempt":     func(d *bridgeDocument) { d.Attempt = "\n" },
		"duplicate category":  func(d *bridgeDocument) { d.Categories = append(d.Categories, d.Categories[0]) },
		"empty category name": func(d *bridgeDocument) { d.Categories[0].Name = " " },
	} {
		t.Run(name, func(t *testing.T) {
			d := valid()
			change(&d)
			if validBridgeDocument(d) {
				t.Fatal("invalid document accepted")
			}
		})
	}
}
func startProWorker(t *testing.T, s *Server) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.RunAIWritingQueue(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}
func TestProPublicationMetadataTopicsAndIsolation(t *testing.T) {
	s, h, _, cookie, actor := newCommentHTTPTestServer(t)
	enableWritingFixture(t, s)
	st, _ := s.aiJobs.Snapshot()
	rules := s.defaultProRules(st.Rules, s.aiSettings.Snapshot())
	rules.Author = actor.Username
	rules.AutoPost = true
	rules.Topics = true
	if err := s.aiJobs.SaveRules(st.Epoch, rules); err != nil {
		t.Fatal(err)
	}
	in := content.PostInput{Type: "post", Status: "publish", Title: "测试资料", Slug: "manual-slug", Excerpt: "手工摘要", ContentSource: "<p>一些参考信息。</p>", AIPolicy: "off"}
	post, err := s.repository.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := s.aiJobs.Snapshot()
	if len(state.Jobs) != 0 {
		t.Fatal("opt out ignored")
	}
	in.AIPolicy = "on"
	post, err = s.repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: actor.Username}, in)
	if err != nil {
		t.Fatal(err)
	}
	setAIResponse(s, 200, "text/event-stream", aiFrame(`{"excerpt":"不能覆盖","tags":["资料"],"slug":"other","topics":[{"title":"资料如何核对","body":"阅读原文。"}]}`, "stop")+"data: [DONE]\n\n", nil)
	startProWorker(t, s)
	waitForProJob(t, s, "enrich", "succeeded")
	p, _ := s.repository.GetByID(t.Context(), post.ID)
	topics := relatedTopics(p)
	if p.Version != post.Version+1 || p.Excerpt != in.Excerpt || p.Slug != in.Slug || len(p.Tags) != 1 || len(topics) != 1 {
		t.Fatalf("bad patch: %+v", p)
	}
	state, _ = s.aiJobs.Snapshot()
	if len(state.Jobs) != 1 {
		t.Fatal("self-trigger")
	}
	url := "https://example.test/ai-topics/" + strconv.FormatInt(p.ID, 10) + "/" + topics[0].ID + "/"
	get := func() int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		return w.Code
	}
	editorURL := "https://example.test/admin/plugins/ai-pro-max/articles/" + strconv.FormatInt(p.ID, 10) + "/topics/"
	view := serveCommentRequest(h, cookie, "GET", editorURL, "", "", "8.8.8.8:80")
	if view.Code != 200 || !strings.Contains(view.Body.String(), "资料如何核对") {
		t.Fatalf("topic editor %d", view.Code)
	}
	if get() != 200 {
		t.Fatal("public topic missing")
	}
	_, err = s.repository.ChangeStatus(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version, Actor: actor.Username}, "private")
	if err != nil || get() != 404 {
		t.Fatal("private topic exposed", err)
	}
}
func TestProEnrichmentConcurrentEditCannotOverwrite(t *testing.T) {
	s, _, _, _, actor := newCommentHTTPTestServer(t)
	enableWritingFixture(t, s)
	st, _ := s.aiJobs.Snapshot()
	r := s.defaultProRules(st.Rules, s.aiSettings.Snapshot())
	r.Author = actor.Username
	r.AutoPost = true
	if err := s.aiJobs.SaveRules(st.Epoch, r); err != nil {
		t.Fatal(err)
	}
	p, err := s.repository.Create(t.Context(), content.PostInput{Title: "竞争", Slug: "race", ContentSource: "<p>原始资料</p>", Status: "publish"})
	if err != nil {
		t.Fatal(err)
	}
	setAIResponse(s, 200, "text/event-stream", aiFrame(`{"excerpt":"模型摘要","tags":["词"]}`, "stop")+"data: [DONE]\n\n", func(*http.Request) {
		in := inputFromPost(p)
		in.Excerpt = "编辑刚刚写入"
		in.AIPolicy = "off"
		_, err := s.repository.Update(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version, Actor: "editor"}, in)
		if err != nil {
			t.Error(err)
		}
	})
	startProWorker(t, s)
	waitForProJob(t, s, "enrich", "failed")
	p, _ = s.repository.GetByID(t.Context(), p.ID)
	if p.Excerpt != "编辑刚刚写入" {
		t.Fatal("clobbered concurrent edit")
	}
}
func TestProModerationAndScheduledReplyDoNotLoop(t *testing.T) {
	s, _, store, _, actor := newCommentHTTPTestServer(t)
	enableWritingFixture(t, s)
	st, _ := s.aiJobs.Snapshot()
	r := s.defaultProRules(st.Rules, s.aiSettings.Snapshot())
	r.Author = actor.Username
	r.Moderate = true
	r.WakeWords = "@助手"
	r.DelayMinutes = 30
	if err := s.aiJobs.SaveRules(st.Epoch, r); err != nil {
		t.Fatal(err)
	}
	p, err := s.repository.Create(t.Context(), content.PostInput{Title: "讨论", Slug: "discussion-test", Status: "publish", CommentStatus: "open"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := store.Create(t.Context(), comment.CreateInput{PostID: p.ID, AuthorName: "用户", AuthorEmail: "test@example.org", Status: "pending", ContentSource: "@助手 怎样使用？"})
	if err != nil {
		t.Fatal(err)
	}
	s.enqueueComment(t.Context(), c.ID)
	setAIResponse(s, 200, "text/event-stream", aiFrame(`{"decision":"approved","reply":"可以从原文的步骤开始。"}`, "stop")+"data: [DONE]\n\n", nil)
	startProWorker(t, s)
	j := waitForProJob(t, s, "release", "queued")
	if !j.Due.After(time.Now().Add(29 * time.Minute)) {
		t.Fatal("ignored delay")
	}
	if err := s.aiJobs.Action(st.Epoch, j.ID, j.Version, "advance", time.Now()); err != nil {
		t.Fatal(err)
	}
	waitForProJob(t, s, "release", "succeeded")
	items, _, err := store.ListApproved(t.Context(), p.ID, 20, 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("replies %+v %v", items, err)
	}
	ai := false
	for _, v := range items {
		if v.ParentID == c.ID && strings.HasPrefix(v.AuthorName, "AI ·") {
			ai = true
		}
	}
	if !ai {
		t.Fatal("missing labelled AI reply")
	}
	snapshot, _ := s.aiJobs.Snapshot()
	if len(snapshot.Jobs) != 1 {
		t.Fatal("reply recursively queued")
	}
}
func TestProBridgeRequiresExplicitPairing(t *testing.T) {
	s, h, _, _, _ := newCommentHTTPTestServer(t)
	enableWritingFixture(t, s)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/ai-pro-max/bridge/invalid", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	st, _ := s.aiJobs.Snapshot()
	if err := s.aiJobs.Commit(t.Context(), aijobs.Job{}, func() error { return errors.New("unexpected write") }); !errors.Is(err, aijobs.ErrStale) || len(st.Jobs) > 0 {
		t.Fatal(err)
	}
}

func TestProBridgeRoundTripKeepsPermittedFields(t *testing.T) {
	s, h, _, _, actor := newCommentHTTPTestServer(t)
	enableWritingFixture(t, s)
	st, _ := s.aiJobs.Snapshot()
	rules := s.defaultProRules(st.Rules, s.aiSettings.Snapshot())
	rules.External = true
	rules.Author = actor.Username
	if err := s.aiJobs.SaveRules(st.Epoch, rules); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("b", 40)
	t.Setenv("NEWPRESS_AI_BRIDGE_TOKEN", key)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	in := bridgeDocument{Site: "https://store.example", ID: 23, Type: "product", Title: "站内商品", Body: "说明", Revision: strings.Repeat("a", 64), Fields: []string{"excerpt", "tags", "slug"}}
	raw, _ := json.Marshal(in)
	w := call("POST", "/api/v1/ai-pro-max/bridge", string(raw))
	if w.Code != 202 {
		t.Fatalf("pair submit %d %s", w.Code, w.Body)
	}
	var queued struct{ Ticket string }
	if json.Unmarshal(w.Body.Bytes(), &queued) != nil || len(queued.Ticket) != 64 {
		t.Fatal(w.Body.String())
	}
	ackPath := "/api/v1/ai-pro-max/bridge/" + queued.Ticket + "/ack"
	if ack := call("POST", ackPath, ""); ack.Code != http.StatusConflict {
		t.Fatal("unfinished result was acknowledged", ack.Code)
	}
	setAIResponse(s, 200, "text/event-stream", aiFrame(`{"excerpt":"商品摘要","slug":"keep-existing","tags":["商品"]}`, "stop")+"data: [DONE]\n\n", nil)
	startProWorker(t, s)
	waitForProJob(t, s, "external", "succeeded")
	res := call("GET", "/api/v1/ai-pro-max/bridge/"+queued.Ticket, "")
	var out struct {
		Status string
		Result aiEnrichment
	}
	if json.Unmarshal(res.Body.Bytes(), &out) != nil || res.Code != 200 || out.Status != "succeeded" || out.Result.Excerpt != "商品摘要" || out.Result.Slug != "" {
		t.Fatal(res.Code, res.Body.String())
	}
	for range 2 {
		if ack := call("POST", ackPath, ""); ack.Code != http.StatusOK {
			t.Fatal("acknowledgement was not idempotent", ack.Code, ack.Body.String())
		}
	}
	state, _ := s.aiJobs.Snapshot()
	if !state.Jobs[0].Work.Collected {
		t.Fatal("collected result still blocks queue rotation")
	}
}

func TestProBridgeDraftFieldsAndRequestIdentity(t *testing.T) {
	s, h, _, _, actor := newCommentHTTPTestServer(t)
	enableWritingFixture(t, s)
	st, _ := s.aiJobs.Snapshot()
	rules := s.defaultProRules(st.Rules, s.aiSettings.Snapshot())
	rules.External, rules.Author = true, actor.Username
	if err := s.aiJobs.SaveRules(st.Epoch, rules); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("b", 40)
	t.Setenv("NEWPRESS_AI_BRIDGE_TOKEN", key)
	in := bridgeDocument{Site: "https://store.example", ID: 23, Type: "product", Status: "draft", Title: "商品草稿", Revision: strings.Repeat("a", 64), Fields: []string{"slug", "categoryID"}, Categories: []aieditor.Category{{ID: 7, Name: "商品"}}, Attempt: "request-first"}
	submit := func() (int, string) {
		t.Helper()
		raw, _ := json.Marshal(in)
		req := httptest.NewRequest("POST", "/api/v1/ai-pro-max/bridge", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var response struct{ Ticket string }
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return w.Code, response.Ticket
	}
	code, ticket := submit()
	if code != http.StatusAccepted || len(ticket) != 64 {
		t.Fatal(code, ticket)
	}
	code, duplicate := submit()
	if code != http.StatusAccepted || duplicate != ticket {
		t.Fatal("transport retry changed ticket", code, duplicate)
	}
	state, _ := s.aiJobs.Snapshot()
	if len(state.Jobs) != 1 {
		t.Fatal("duplicate submission queued twice")
	}
	var source bridgeDocument
	if json.Unmarshal([]byte(state.Jobs[0].Work.Source), &source) != nil || len(source.Fields) != 2 {
		t.Fatal("draft fields were protected as published content", source)
	}
	in.Attempt = "request-retry"
	code, retry := submit()
	if code != http.StatusAccepted || retry == ticket {
		t.Fatal("explicit retry reused the previous ticket", code, retry)
	}
	in.Status = "publish"
	code, published := submit()
	if code != http.StatusOK || published != "" {
		t.Fatal("safe update allowed published slug/category changes", code, published)
	}
	in.Status = "draft"
	in.Fields = []string{"unsupported"}
	code, _ = submit()
	if code != http.StatusUnprocessableEntity {
		t.Fatal("invalid bridge input was silently accepted", code)
	}
}
