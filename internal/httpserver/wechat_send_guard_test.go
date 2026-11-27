package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

func TestWeChatDisableDuringTokenWaitThenExplicitDraftRetry(t *testing.T) {
	s, handler, _, repo, cookie, owner := newPersistentAdminTestServer(t)
	const base = "https://example.test/api/v1/admin/wechat"
	headers := authenticatedAPIHeaders(owner)
	storePath := filepath.Join(t.TempDir(), "wechat.json")
	store, err := wechat.NewFileStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	s.wechat.Store = store
	cfg := wechat.Defaults()
	cfg.Enabled, cfg.AutoPublish = true, true
	cfg.AppID, cfg.Secret, cfg.ThumbMediaID = "wx0123456789abcdef", strings.Repeat("s", 32), "cover"
	save := func(next wechat.Config) wechat.Config {
		t.Helper()
		response := serveContentAPI(handler, cookie, "POST", base+"/config", wechatJSON(t, map[string]any{"config": next}), headers)
		var result struct {
			Config wechat.Config `json:"config"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 {
			t.Fatal("configuration save failed", response.Code, response.Body.String(), err)
		}
		return result.Config
	}
	cfg = save(cfg)
	post, err := repo.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Slug: "token-checkpoint", Title: "发送前停用", ContentSource: "<p>正式保存的正文</p>", AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	response := serveContentAPI(handler, cookie, "POST", base+"/sync", wechatJSON(t, map[string]any{"post_id": post.ID, "version": post.Version, "config_version": cfg.Version}), headers)
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
	armed := true
	calls := map[string]int{}
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		calls[req.URL.Path]++
		body := ""
		switch req.URL.Path {
		case "/cgi-bin/stable_token":
			if armed {
				cfg.Enabled = false
				cfg = save(cfg)
			}
			body = `{"access_token":"private-token","expires_in":7200}`
		case "/cgi-bin/draft/add":
			body = `{"media_id":"confirmed-draft"}`
		case "/cgi-bin/freepublish/submit":
			body = `{"publish_id":123}`
		default:
			t.Fatalf("unexpected request: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	readJob := func() wechat.Job {
		t.Helper()
		response := serveContentAPI(handler, cookie, "GET", base, "", nil)
		var result struct {
			Jobs []wechat.Job `json:"jobs"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || len(result.Jobs) != 1 || strings.Contains(response.Body.String(), "private-token") {
			t.Fatal("job state unavailable", response.Code, response.Body.String(), err)
		}
		return result.Jobs[0]
	}
	if err = s.wechat.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	job := readJob()
	if job.Status != "failed" || !strings.Contains(job.Error, "配置已变化") || job.MediaID != "" || job.PublishID != "" || calls["/cgi-bin/draft/add"] != 0 || calls["/cgi-bin/freepublish/submit"] != 0 {
		t.Fatal("disabled synchronization still wrote after token acquisition", job, calls)
	}
	reopened, err := wechat.NewFileStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	s.wechat.Store = reopened
	if restored := readJob(); restored.Status != "failed" || restored.ID != job.ID || restored.MediaID != "" {
		t.Fatal("failure receipt changed after restart", restored)
	}
	armed = false
	cfg.Enabled, cfg.AutoPublish = true, false
	cfg = save(cfg)
	if err = s.wechat.Tick(t.Context(), time.Now().Add(time.Minute)); err != nil || calls["/cgi-bin/draft/add"] != 0 {
		t.Fatal("reenabling retried automatically", err, calls)
	}
	// Existing task policy changes only through an explicit action, then retry.
	for _, action := range []string{"draft_only", "retry"} {
		job = readJob()
		response := serveContentAPI(handler, cookie, "POST", base+"/jobs/"+job.ID, wechatJSON(t, map[string]any{"action": action, "updated_at": job.UpdatedAt}), headers)
		if response.Code != 200 {
			t.Fatal(action, response.Code, response.Body.String())
		}
	}
	if err = s.wechat.Tick(t.Context(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	job = readJob()
	if job.Status != "draft" || job.AutoPublish || job.MediaID != "confirmed-draft" || job.PublishID != "" || job.Error != "" || calls["/cgi-bin/draft/add"] != 1 || calls["/cgi-bin/freepublish/submit"] != 0 || calls["/cgi-bin/stable_token"] != 1 {
		t.Fatal("explicit draft retry did not preserve policy or token reuse", job, calls)
	}
}
