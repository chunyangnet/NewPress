package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

func TestWeChatSyncOriginalArticleLinksReachDraftAndPersist(t *testing.T) {
	s, handler, _, repo, cookie, owner := newPersistentAdminTestServer(t)
	storePath := filepath.Join(t.TempDir(), "wechat.json")
	store, err := wechat.NewFileStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	s.wechat.Store, s.wechat.PublicURL = store, "https://example.test"
	cfg := wechat.Defaults()
	cfg.Enabled, cfg.AppID, cfg.Secret, cfg.ThumbMediaID = true, "wx0123456789abcdef", strings.Repeat("s", 32), "cover"
	cfg, err = s.wechat.SaveConfig(t.Context(), cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	post, err := repo.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish,
		Title: "同步文章链接", Slug: "outbound-links", AuthorID: owner.UserID, PublishedAt: time.Now().Add(-time.Hour),
		ContentSource: `<p><a href="next?x=1&amp;y=2">下一篇</a><a href="#chapter">章节</a><a href="?view=print">打印</a><a href="https://outside.test/read">外链</a></p>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sent wechat.Article
	var calls []string
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.URL.Path)
		body := ""
		switch req.URL.Path {
		case "/cgi-bin/stable_token":
			body = `{"access_token":"fixture-token","expires_in":7200}`
		case "/cgi-bin/draft/add":
			var payload struct {
				Articles []wechat.Article `json:"articles"`
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || len(payload.Articles) != 1 {
				t.Fatal("invalid outgoing draft", err)
			}
			sent = payload.Articles[0]
			body = `{"media_id":"fixture-linked-draft"}`
		default:
			t.Fatalf("unexpected remote write: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}
	const base = "https://example.test/api/v1/admin/wechat"
	body := wechatJSON(t, map[string]any{"post_id": post.ID, "version": post.Version, "config_version": cfg.Version})
	headers := authenticatedAPIHeaders(owner)
	response := serveContentAPI(handler, cookie, "POST", base+"/sync/check", body, headers)
	var checked struct {
		Check wechat.SyncCheck `json:"check"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &checked); err != nil || response.Code != 200 || !checked.Check.Ready || len(calls) != 0 {
		t.Fatal("local HTTP check", response.Code, response.Body.String(), err, calls)
	}
	response = serveContentAPI(handler, cookie, "POST", base+"/sync", body, headers)
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
	if err = s.wechat.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"https://example.test/posts/next?x=1&amp;y=2", "https://example.test/posts/outbound-links#chapter", "https://example.test/posts/outbound-links?view=print", "https://outside.test/read"} {
		if !strings.Contains(sent.Content, `href="`+target+`"`) {
			t.Errorf("draft lost target %q: %s", target, sent.Content)
		}
	}
	if checked.Check.ContentChars != utf8.RuneCountInString(sent.Content) || checked.Check.ContentBytes != len(sent.Content) || sent.SourceURL != "https://example.test/posts/outbound-links" {
		t.Fatal("preflight and transmitted draft differ", checked.Check, sent)
	}
	if !reflect.DeepEqual(calls, []string{"/cgi-bin/stable_token", "/cgi-bin/draft/add"}) {
		t.Fatal("draft-only policy changed", calls)
	}
	reopened, err := wechat.NewFileStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	state, err := reopened.View(t.Context())
	if err != nil || len(state.Jobs) != 1 || state.Jobs[0].Status != "draft" || state.Jobs[0].MediaID != "fixture-linked-draft" {
		t.Fatal("draft receipt not preserved", state, err)
	}
	s.wechat.Store = reopened
	if err = s.wechat.Tick(t.Context(), time.Now().Add(time.Hour)); err != nil || len(calls) != 2 {
		t.Fatal("restart resent completed draft", err, calls)
	}
	after, err := repo.GetByID(t.Context(), post.ID)
	if err != nil || !reflect.DeepEqual(after, post) {
		t.Fatal("sync rewrote source article", err)
	}
}
