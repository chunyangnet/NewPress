package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

func TestWeChatLocalCheckHTTPAndPermissions(t *testing.T) {
	s, h, users, _, cookie, owner := newPersistentAdminTestServer(t)
	_, authorCookie, author := createAndLoginTestUser(t, users, owner, "check-author", "check-author@example.test", "作者", "author")
	cfg := wechat.Defaults()
	cfg.Enabled = true
	cfg.AppID = "wx0123456789abcdef"
	cfg.Secret = strings.Repeat("s", 32)
	cfg.ThumbMediaID = "cover"
	if _, err := s.wechat.SaveConfig(t.Context(), cfg, false); err != nil {
		t.Fatal(err)
	}
	post, err := s.repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "检查文章", Slug: "wechat-check", ContentSource: "检查正文", AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.wechat.Store.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
		t.Error("inspection called external API", r.URL.Path)
		return nil, errors.New("unexpected remote request")
	})}
	endpoint := "https://example.test/api/v1/admin/wechat/sync/check"
	input := map[string]any{"post_id": post.ID, "version": post.Version, "config_version": before.Config.Version}
	body := wechatJSON(t, input)
	headers := authenticatedAPIHeaders(owner)
	for _, row := range []struct {
		cookie  *http.Cookie
		headers map[string]string
		want    int
	}{{nil, headers, 401}, {authorCookie, authenticatedAPIHeaders(author), 403}, {cookie, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"}, 403}} {
		r := serveContentAPI(h, row.cookie, "POST", endpoint, body, row.headers)
		if r.Code != row.want {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	r := serveContentAPI(h, cookie, "POST", endpoint, body, headers)
	var response struct {
		Check wechat.SyncCheck `json:"check"`
	}
	if err = json.Unmarshal(r.Body.Bytes(), &response); err != nil || r.Code != 200 || !response.Check.Ready || response.Check.PostID != post.ID || r.Header().Get("Cache-Control") != "no-store" || strings.Contains(r.Body.String(), cfg.Secret) {
		t.Fatal(r.Code, r.Body.String(), err)
	}
	after, err := s.wechat.Store.View(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("check created a task or changed configuration", err)
	}
	input["version"] = post.Version + 1
	if r = serveContentAPI(h, cookie, "POST", endpoint, wechatJSON(t, input), headers); r.Code != 409 {
		t.Fatal("stale article check", r.Code, r.Body.String())
	}
	for _, bad := range []string{`{"post_id":1}`, `{"post_id":1,"post_id":1,"version":1,"config_version":2}`, `{"post_id":1,"version":1,"config_version":2,"extra":true}`} {
		if r = serveContentAPI(h, cookie, "POST", endpoint, bad, headers); r.Code != 400 {
			t.Fatal("invalid check request", r.Code, r.Body.String())
		}
	}
	post, err = s.repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: strings.Repeat("长", 33), Slug: post.Slug, ContentSource: "正文", AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	input["version"] = post.Version
	r = serveContentAPI(h, cookie, "POST", endpoint, wechatJSON(t, input), headers)
	if err = json.Unmarshal(r.Body.Bytes(), &response); err != nil || r.Code != 200 || response.Check.Ready || len(response.Check.Issues) != 1 || response.Check.Issues[0].Field != "title" {
		t.Fatal(r.Code, r.Body.String(), err)
	}
}
