package httpserver

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

type wechatTransport func(*http.Request) (*http.Response, error)

func (f wechatTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func wechatJSON(t *testing.T, v any) string {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestWeChatAdminConfigQueueAndSecurity(t *testing.T) {
	s, h, users, _, cookie, owner := newPersistentAdminTestServer(t)
	_, authorCookie, author := createAndLoginTestUser(t, users, owner, "wechat-author", "wechat-author@example.test", "作者", "author")
	base := "https://example.test/api/v1/admin/wechat"
	headers := authenticatedAPIHeaders(owner)
	for _, row := range []struct {
		cookie *http.Cookie
		want   int
	}{{nil, 401}, {authorCookie, 403}} {
		r := serveContentAPI(h, row.cookie, "GET", base, "", nil)
		if r.Code != row.want {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	r := serveContentAPI(h, cookie, "GET", base, "", nil)
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(r.Code, r.Body.String())
	}
	var data struct {
		Config wechat.Config `json:"config"`
	}
	if e := json.Unmarshal(r.Body.Bytes(), &data); e != nil {
		t.Fatal(e)
	}
	data.Config.Enabled = true
	data.Config.AppID = "wx0123456789abcdef"
	data.Config.Secret = strings.Repeat("s", 32)
	data.Config.ThumbMediaID = "cover"
	body := wechatJSON(t, map[string]any{"config": data.Config})
	for _, row := range []struct {
		cookie  *http.Cookie
		headers map[string]string
		want    int
	}{{nil, headers, 401}, {authorCookie, authenticatedAPIHeaders(author), 403}, {cookie, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"}, 403}} {
		r = serveContentAPI(h, row.cookie, "POST", base+"/config", body, row.headers)
		if r.Code != row.want {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	foreign := authenticatedAPIHeaders(owner)
	foreign["Origin"] = "https://other.test"
	if r = serveContentAPI(h, cookie, "POST", base+"/config", body, foreign); r.Code != 403 {
		t.Fatal(r.Code)
	}
	r = serveContentAPI(h, cookie, "POST", base+"/config", body, headers)
	if r.Code != 200 || strings.Contains(r.Body.String(), "app_secret") || strings.Contains(r.Body.String(), data.Config.Secret) {
		t.Fatal(r.Code, r.Body.String())
	}
	if r = serveContentAPI(h, cookie, "POST", base+"/config", body, headers); r.Code != 409 {
		t.Fatal("stale config", r.Code)
	}
	for _, bad := range []string{`{"config":{},"config":{}}`, `{"config":{"version":null}}`, `{"unexpected":true}`} {
		if r = serveContentAPI(h, cookie, "POST", base+"/config", bad, headers); r.Code != 400 {
			t.Fatal("strict JSON", r.Code, r.Body.String())
		}
	}
	post, e := s.repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "微信公众号集成", Slug: "wechat-admin-test", ContentSource: "正文", AuthorID: owner.UserID})
	if e != nil {
		t.Fatal(e)
	}
	current, _ := s.wechat.Store.View(t.Context())
	input := map[string]any{"post_id": post.ID, "version": post.Version, "config_version": current.Config.Version}
	input["config_version"] = current.Config.Version - 1
	if r = serveContentAPI(h, cookie, "POST", base+"/sync", wechatJSON(t, input), headers); r.Code != 409 {
		t.Fatal("stale publication mode", r.Code, r.Body.String())
	}
	input["config_version"] = current.Config.Version
	for i := 0; i < 2; i++ {
		r = serveContentAPI(h, cookie, "POST", base+"/sync", wechatJSON(t, input), headers)
		if r.Code != 202 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	v, e := s.wechat.Store.View(t.Context())
	if e != nil || len(v.Jobs) != 1 {
		t.Fatal("duplicate sync", v, e)
	}
	input["version"] = post.Version + 1
	if r = serveContentAPI(h, cookie, "POST", base+"/sync", wechatJSON(t, input), headers); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	r = serveContentAPI(h, cookie, "GET", "https://example.test/admin/wechat/", "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "chunnel-wechat.js") || !strings.Contains(r.Body.String(), "chunnel-wechat.css") {
		t.Fatal("page", r.Code, r.Body.String())
	}
	r = serveContentAPI(h, nil, "GET", "https://example.test/", "", nil)
	if strings.Contains(r.Body.String(), "chunnel-wechat") {
		t.Fatal("admin assets on homepage")
	}
	// Connection testing uses only the configured credentials; no API response leaks them.
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() != "api.weixin.qq.com" || req.URL.Path != "/cgi-bin/stable_token" {
			t.Fatal(req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"private-token","expires_in":7200}`)), Request: req}, nil
	})}
	r = serveContentAPI(h, cookie, "POST", base+"/test", "{}", headers)
	if r.Code != 200 || strings.Contains(r.Body.String(), "private-token") {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestWeChatCollectPreviewApplyAndTamper(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	base := "https://example.test/api/v1/admin/wechat/collect/"
	headers := authenticatedAPIHeaders(owner)
	var picture bytes.Buffer
	_ = png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 16, 16)))
	articleRequests, imageRequests := 0, 0
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		var body io.Reader
		switch req.URL.Hostname() {
		case "mp.weixin.qq.com":
			articleRequests++
			body = strings.NewReader(`<html><head><meta property="og:title" content="采集标题"></head><body><div id="js_name">来源公众号</div><div id="js_content"><p>正文<script>alert(1)</script></p><img data-src="https://mmbiz.qpic.cn/picture"></div></body></html>`)
		case "mmbiz.qpic.cn":
			imageRequests++
			body = bytes.NewReader(picture.Bytes())
		default:
			t.Fatal("unexpected host", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(body), Request: req}, nil
	})}
	previewInput := map[string]any{"urls": []string{"https://mp.weixin.qq.com/s/abcdef", "https://mp.weixin.qq.com/s/abcdef?scene=1", "http://127.0.0.1/private"}}
	r := serveContentAPI(h, cookie, "POST", base+"preview", wechatJSON(t, previewInput), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var preview struct {
		Articles []wechat.Collected      `json:"articles"`
		Receipts map[string]string       `json:"receipts"`
		Plan     content.ImportPlan      `json:"plan"`
		Failures []wechat.CollectFailure `json:"failures"`
	}
	if e := json.Unmarshal(r.Body.Bytes(), &preview); e != nil {
		t.Fatal(e)
	}
	if len(preview.Articles) != 1 || len(preview.Receipts) != 1 || preview.Plan.New != 1 || len(preview.Failures) != 1 || imageRequests != 0 {
		t.Fatal(preview, imageRequests)
	}
	var responseFields map[string]json.RawMessage
	if err := json.Unmarshal(r.Body.Bytes(), &responseFields); err != nil {
		t.Fatal(err)
	}
	if _, exists := responseFields["token"]; exists {
		t.Fatal("preview returned a retired whole-batch token")
	}
	apply := func(articles []wechat.Collected, receipts map[string]string) string {
		return wechatJSON(t, map[string]any{"articles": articles, "receipts": receipts})
	}
	tampered := append([]wechat.Collected(nil), preview.Articles...)
	tampered[0].Title = "篡改"
	if r = serveContentAPI(h, cookie, "POST", base+"apply", apply(tampered, preview.Receipts), headers); r.Code != 409 {
		t.Fatal("tamper", r.Code)
	}
	key := preview.Articles[0].Key
	claim, valid := s.verifyImport(preview.Receipts[key])
	if !valid {
		t.Fatal("invalid preview receipt")
	}
	expired := claim
	expired.Expires = time.Now().Add(-time.Second).Unix()
	if r = serveContentAPI(h, cookie, "POST", base+"apply", apply(preview.Articles, map[string]string{key: s.signImport(expired)}), headers); r.Code != 409 {
		t.Fatal("expiry", r.Code)
	}
	other := claim
	other.UserID = owner.UserID + 1
	if r = serveContentAPI(h, cookie, "POST", base+"apply", apply(preview.Articles, map[string]string{key: s.signImport(other)}), headers); r.Code != 409 {
		t.Fatal("identity", r.Code)
	}
	if r = serveContentAPI(h, cookie, "POST", base+"apply", apply(preview.Articles, nil), headers); r.Code != 409 {
		t.Fatal("missing receipts", r.Code)
	}
	retiredToken := s.signImport(importClaims{UserID: owner.UserID, Digest: "wechat:" + collectedDigest(preview.Articles), Revision: preview.Plan.Revision, Expires: claim.Expires})
	for _, body := range []map[string]any{
		{"articles": preview.Articles, "token": retiredToken},
		{"articles": preview.Articles, "receipts": preview.Receipts, "token": retiredToken},
	} {
		if r = serveContentAPI(h, cookie, "POST", base+"apply", wechatJSON(t, body), headers); r.Code != 400 {
			t.Fatal("retired token field accepted", r.Code, r.Body.String())
		}
	}
	if r = serveContentAPI(h, cookie, "POST", base+"preview", `{"urls":[],"urls":[]}`, headers); r.Code != 400 {
		t.Fatal("duplicate JSON", r.Code)
	}
	if imageRequests != 0 {
		t.Fatal("download before authorized apply")
	}
	r = serveContentAPI(h, cookie, "POST", base+"apply", apply(preview.Articles, preview.Receipts), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var result struct {
		Plan content.ImportPlan `json:"plan"`
	}
	if e := json.Unmarshal(r.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	post, e := s.repository.GetByID(t.Context(), result.Plan.Items[0].PostID)
	if e != nil || post.Status != content.StatusDraft || imageRequests != 1 || strings.Contains(post.ContentHTML, "<script") || !strings.Contains(post.ContentHTML, "/media/") {
		t.Fatal(post, e, imageRequests)
	}
	input := content.PostInput{Type: post.Type, Status: content.StatusDraft, Title: "本站人工修改", Slug: post.Slug, ContentSource: "新正文", AuthorID: owner.UserID}
	if _, e = s.repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, input); e != nil {
		t.Fatal(e)
	}
	r = serveContentAPI(h, cookie, "POST", base+"preview", wechatJSON(t, previewInput), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if e = json.Unmarshal(r.Body.Bytes(), &preview); e != nil {
		t.Fatal(e)
	}
	if preview.Plan.New != 0 || preview.Plan.Existing != 1 {
		t.Fatal(preview.Plan)
	}
	r = serveContentAPI(h, cookie, "POST", base+"apply", apply(preview.Articles, preview.Receipts), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	post, e = s.repository.GetByID(t.Context(), post.ID)
	if e != nil || post.Title != "本站人工修改" || imageRequests != 1 {
		t.Fatal("overwritten or redownloaded", post, e)
	}
	v, _ := s.wechat.Store.View(t.Context())
	v.Config.CollectEnabled = false
	if _, e = s.wechat.SaveConfig(t.Context(), v.Config, false); e != nil {
		t.Fatal(e)
	}
	before := articleRequests
	r = serveContentAPI(h, cookie, "POST", base+"preview", wechatJSON(t, previewInput), headers)
	if r.Code != 422 || articleRequests != before {
		t.Fatal("disabled module fetched", r.Code)
	}
}
