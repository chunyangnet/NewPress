package httpserver

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

func TestWeChatReceiptReconciliationHTTP(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	base := "https://example.test/api/v1/admin/wechat"
	headers := authenticatedAPIHeaders(owner)
	cfg := wechat.Defaults()
	cfg.Enabled, cfg.AutoPublish = true, true
	cfg.AppID, cfg.Secret, cfg.ThumbMediaID = "wx0123456789abcdef", strings.Repeat("s", 32), "cover"
	if _, err := s.wechat.SaveConfig(t.Context(), cfg, false); err != nil {
		t.Fatal(err)
	}
	post, err := s.repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "回执 HTTP 验证", Slug: "receipt-http", ContentSource: "正文", AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.wechat.Store.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	request := wechatJSON(t, map[string]any{"post_id": post.ID, "version": post.Version, "config_version": state.Config.Version})
	if r := serveContentAPI(h, cookie, "POST", base+"/sync", request, headers); r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	calls := map[string]int{}
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
		calls[r.URL.Path]++
		var body string
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			body = `{"access_token":"private-token","expires_in":7200}`
		case "/cgi-bin/draft/add":
			body = `{"media_id":"draft"}`
		case "/cgi-bin/freepublish/submit":
			body = `{"publish_id":null}`
		case "/cgi-bin/freepublish/get":
			body = `{"publish_id":"verified-task","publish_status":0,"article_id":"article"}`
		default:
			t.Fatal("unexpected remote request", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	if err = s.wechat.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	state, err = s.wechat.Store.View(t.Context())
	if err != nil || len(state.Jobs) != 1 || state.Jobs[0].Status != "uncertain" {
		t.Fatal(state, err)
	}
	job := state.Jobs[0]
	result := serveContentAPI(h, cookie, "GET", base, "", nil)
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"status":"uncertain"`) || strings.Contains(result.Body.String(), "private-token") || result.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(result.Code, result.Body.String())
	}
	jobURL := base + "/jobs/" + job.ID
	for _, id := range []string{"wrong\tID", "wrong\u00a0ID"} {
		body := wechatJSON(t, map[string]any{"action": "resolve", "updated_at": job.UpdatedAt, "publish_id": id})
		if result = serveContentAPI(h, cookie, "POST", jobURL, body, headers); result.Code != 422 {
			t.Fatal("invalid manual receipt accepted", result.Code, result.Body.String())
		}
	}
	// Operators can reconcile known results while the module is disabled;
	// polling resumes only after the module is enabled again.
	cfg = state.Config
	cfg.Enabled = false
	if _, err = s.wechat.SaveConfig(t.Context(), cfg, false); err != nil {
		t.Fatal(err)
	}
	body := wechatJSON(t, map[string]any{"action": "resolve", "updated_at": job.UpdatedAt, "publish_id": "verified-task"})
	if result = serveContentAPI(h, cookie, "POST", jobURL, body, headers); result.Code != 200 {
		t.Fatal(result.Code, result.Body.String())
	}
	if err = s.wechat.Tick(t.Context(), time.Now().Add(time.Hour)); err != nil || calls["/cgi-bin/freepublish/get"] != 0 {
		t.Fatal("disabled module polled", err, calls)
	}
	state, err = s.wechat.Store.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg = state.Config
	cfg.Enabled = true
	if _, err = s.wechat.SaveConfig(t.Context(), cfg, false); err != nil {
		t.Fatal(err)
	}
	if err = s.wechat.Tick(t.Context(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	result = serveContentAPI(h, cookie, "GET", base, "", nil)
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"status":"published"`) || !strings.Contains(result.Body.String(), `"publish_id":"verified-task"`) || calls["/cgi-bin/draft/add"] != 1 || calls["/cgi-bin/freepublish/submit"] != 1 {
		t.Fatal("recovery changed original operation", result.Code, result.Body.String(), calls)
	}
}
