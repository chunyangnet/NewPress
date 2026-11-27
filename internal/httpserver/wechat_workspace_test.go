package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestWeChatHistoryAndEditorSelection(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	const base = "https://example.test/api/v1/admin/wechat"
	if err := s.wechat.Store.Update(t.Context(), func(state *wechat.State) error {
		state.Config.AppID = "wx0123456789abcdef"
		for i := 1; i <= 125; i++ {
			status := "draft"
			if i == 1 {
				status = "queued"
			}
			if i == 2 {
				status = "uncertain"
			}
			state.Jobs = append(state.Jobs, wechat.Job{ID: fmt.Sprintf("%064x", i), AppID: state.Config.AppID, PostID: int64(i), Version: 1, Title: fmt.Sprintf("历史文章 %03d", i), Status: status, Stage: "draft", CreatedAt: time.Now().UTC(), Images: map[string]string{"local": "remote-private"}})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	read := func(query string) wechatJobPage {
		t.Helper()
		r := serveContentAPI(h, cookie, "GET", base+query, "", nil)
		if r.Code != 200 || strings.Contains(r.Body.String(), "remote-private") {
			t.Fatal(r.Code, r.Body.String())
		}
		var page wechatJobPage
		if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := read("")
	if len(page.Jobs) != 20 || page.Jobs[0].PostID != 125 || page.Pages != 7 || page.Active != 1 || page.Attention != 1 {
		t.Fatal(page)
	}
	page = read("?job_page=7")
	if len(page.Jobs) != 5 || page.Jobs[4].PostID != 1 {
		t.Fatal("old jobs inaccessible", page)
	}
	page = read("?job_page=5000&job_status=uncertain")
	if page.Filtered != 1 || page.Page != 1 || page.Jobs[0].PostID != 2 {
		t.Fatal(page)
	}
	page = read("?job_q=123")
	if page.Filtered != 1 || page.Jobs[0].PostID != 123 {
		t.Fatal(page)
	}
	for _, query := range []string{"?job_status=unknown", "?job_page=0", "?job_page=5001", "?job_page=1&job_page=2", "?post=-1", "?post=bad", "?job_q=" + strings.Repeat("x", 101)} {
		if r := serveContentAPI(h, cookie, "GET", base+query, "", nil); r.Code != 400 {
			t.Fatal(query, r.Code)
		}
	}
	post, err := s.repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "旧稿直达", Slug: "wechat-selected-old", ContentSource: "正文", AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 51; i++ {
		if _, err = s.repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "较新文章", Slug: fmt.Sprintf("wechat-newer-%d", i), ContentSource: "正文", AuthorID: owner.UserID}); err != nil {
			t.Fatal(err)
		}
	}
	r := serveContentAPI(h, cookie, "GET", fmt.Sprintf("%s?post=%d&q=unmatched", base, post.ID), "", nil)
	var result struct {
		Selected *struct {
			ID int64 `json:"id"`
		} `json:"selected_post"`
		Posts []any `json:"posts"`
	}
	if err = json.Unmarshal(r.Body.Bytes(), &result); err != nil || r.Code != 200 || result.Selected == nil || result.Selected.ID != post.ID || len(result.Posts) != 0 {
		t.Fatal(r.Code, r.Body.String(), err)
	}
	r = serveContentAPI(h, cookie, "GET", fmt.Sprintf("https://example.test/admin/content/%d/edit/", post.ID), "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), fmt.Sprintf("/admin/wechat/?post=%d", post.ID)) {
		t.Fatal("editor entry missing", r.Code)
	}
	_, err = s.repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: content.StatusPrivate, Title: post.Title, Slug: post.Slug, ContentSource: "私密", AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(h, cookie, "GET", fmt.Sprintf("%s?post=%d", base, post.ID), "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"selected_post":null`) {
		t.Fatal("private selected", r.Code, r.Body.String())
	}
}

func TestWeChatSelectiveCollectionReceipts(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	base := "https://example.test/api/v1/admin/wechat/collect/"
	headers := authenticatedAPIHeaders(owner)
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	downloads := []string{}
	markup := func(id string) string {
		return `<html><meta property="og:title" content="` + id + `"><div id="js_content"><p>正文</p><img data-src="https://mmbiz.qpic.cn/` + id + `"></div></html>`
	}
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		var body io.Reader
		status := 200
		if req.URL.Hostname() == "mp.weixin.qq.com" {
			body = strings.NewReader(markup(strings.TrimPrefix(req.URL.Path, "/s/")))
		} else {
			downloads = append(downloads, req.URL.Path)
			body = bytes.NewReader(picture.Bytes())
			if req.URL.Path == "/second" {
				status = 404
			}
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(body), Request: req}, nil
	})}
	conflict, err := wechat.ParseArticle("https://mp.weixin.qq.com/s/thirdx", markup("thirdx"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Slug: "wechat-" + conflict.Key[:20], Title: "已有目标", ContentSource: "保留", AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	r := serveContentAPI(h, cookie, "POST", base+"preview", `{"urls":["https://mp.weixin.qq.com/s/firstx","https://mp.weixin.qq.com/s/second","https://mp.weixin.qq.com/s/thirdx"]}`, headers)
	var preview struct {
		Articles []wechat.Collected `json:"articles"`
		Receipts map[string]string  `json:"receipts"`
		Expires  string             `json:"expires"`
		Plan     content.ImportPlan `json:"plan"`
	}
	if err = json.Unmarshal(r.Body.Bytes(), &preview); err != nil || r.Code != 200 || len(preview.Receipts) != 2 || preview.Plan.Conflicts != 1 || preview.Expires == "" {
		t.Fatal(r.Code, r.Body.String(), err)
	}
	first, second := preview.Articles[0], preview.Articles[1]
	apply := func(articles []wechat.Collected, receipts map[string]string) int {
		t.Helper()
		r = serveContentAPI(h, cookie, "POST", base+"apply", wechatJSON(t, map[string]any{"articles": articles, "receipts": receipts}), headers)
		return r.Code
	}
	tampered := first
	tampered.Body = "篡改正文"
	if apply([]wechat.Collected{tampered}, map[string]string{first.Key: preview.Receipts[first.Key]}) != 409 {
		t.Fatal("tamper accepted", r.Body.String())
	}
	if apply([]wechat.Collected{first}, map[string]string{first.Key: preview.Receipts[second.Key]}) != 409 {
		t.Fatal("wrong receipt accepted")
	}
	if apply([]wechat.Collected{first, first}, map[string]string{first.Key: preview.Receipts[first.Key]}) != 409 {
		t.Fatal("duplicate selection accepted")
	}
	if apply([]wechat.Collected{first}, preview.Receipts) != 409 {
		t.Fatal("extra receipt accepted")
	}
	if len(downloads) != 0 {
		t.Fatal("download before verification", downloads)
	}
	if apply([]wechat.Collected{first}, map[string]string{first.Key: preview.Receipts[first.Key]}) != 200 {
		t.Fatal("subset blocked by unselected conflict", r.Code, r.Body.String())
	}
	var applied struct {
		Plan content.ImportPlan `json:"plan"`
	}
	if err = json.Unmarshal(r.Body.Bytes(), &applied); err != nil || applied.Plan.New != 1 || len(downloads) != 1 || downloads[0] != "/firstx" {
		t.Fatal(applied, downloads, err)
	}
	post, err := s.repository.GetByID(t.Context(), applied.Plan.Items[0].PostID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: content.StatusDraft, Slug: post.Slug, Title: "保留后续编辑", ContentSource: "修改后的正文", AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if apply([]wechat.Collected{first}, map[string]string{first.Key: preview.Receipts[first.Key]}) != 200 || len(downloads) != 1 {
		t.Fatal("replay changed content or fetched images", r.Code, downloads)
	}
	post, err = s.repository.GetByID(t.Context(), post.ID)
	if err != nil || post.Title != "保留后续编辑" {
		t.Fatal(post, err)
	}
	claim, valid := s.verifyImport(preview.Receipts[second.Key])
	if !valid {
		t.Fatal("receipt invalid")
	}
	claim.Expires = time.Now().Add(-time.Minute).Unix()
	if apply([]wechat.Collected{second}, map[string]string{second.Key: s.signImport(claim)}) != 409 {
		t.Fatal("expired receipt accepted")
	}
	claim.Expires = time.Now().Add(time.Minute).Unix()
	claim.UserID++
	if apply([]wechat.Collected{second}, map[string]string{second.Key: s.signImport(claim)}) != 409 {
		t.Fatal("wrong actor accepted")
	}
	// A competing target created after preview invalidates the selected receipt,
	// before the unavailable image could be downloaded.
	_, err = s.repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Slug: "wechat-" + second.Key[:20], Title: "并发目标", ContentSource: "保留", AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if apply([]wechat.Collected{second}, map[string]string{second.Key: preview.Receipts[second.Key]}) != 409 || len(downloads) != 1 {
		t.Fatal("stale plan accepted", r.Code, downloads)
	}
}
