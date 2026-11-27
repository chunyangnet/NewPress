package httpserver

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

func TestWeChatMetadataReceiptsAndMixedImport(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	const base = "https://example.test/api/v1/admin/wechat/collect/"
	headers := authenticatedAPIHeaders(owner)
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	markup := func(title, excerpt, cover string) string {
		return `<meta property="og:title" content="` + title + `"><meta name="description" content="` + excerpt + `"><meta property="og:image" content="https://mmbiz.qpic.cn/` + cover + `"><div id="js_content"><p>不变的正文</p></div>`
	}
	sources := map[string]string{"/s/modern": markup("第一篇导入", "原摘要", "modern"), "/s/second": markup("第二篇导入", "原摘要", "second"), "/s/freshx": markup("待导入新文", "新文摘要", "freshx")}
	reads := []string{}
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		var body io.Reader
		if req.URL.Hostname() == "mp.weixin.qq.com" {
			body = strings.NewReader(sources[req.URL.Path])
		} else {
			reads = append(reads, req.URL.Path)
			body = bytes.NewReader(picture.Bytes())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(body), Request: req}, nil
	})}
	type response struct {
		Articles []wechat.Collected `json:"articles"`
		Receipts map[string]string  `json:"receipts"`
		Plan     content.ImportPlan `json:"plan"`
	}
	preview := func(paths ...string) response {
		t.Helper()
		urls := []string{}
		for _, path := range paths {
			urls = append(urls, "https://mp.weixin.qq.com"+path)
		}
		r := serveContentAPI(h, cookie, "POST", base+"preview", wechatJSON(t, map[string]any{"urls": urls}), headers)
		var result response
		if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil || r.Code != 200 || len(result.Articles) != len(paths) {
			t.Fatal(r.Code, r.Body.String(), err)
		}
		return result
	}
	apply := func(body map[string]any) content.ImportPlan {
		t.Helper()
		r := serveContentAPI(h, cookie, "POST", base+"apply", wechatJSON(t, body), headers)
		var result response
		if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil || r.Code != 200 {
			t.Fatal(r.Code, r.Body.String(), err)
		}
		return result.Plan
	}
	initial := preview("/s/modern")
	for _, field := range []string{"excerpt", "cover"} {
		articles := append([]wechat.Collected(nil), initial.Articles...)
		if field == "excerpt" {
			articles[0].Excerpt = "篡改摘要"
		} else {
			articles[0].Cover = "https://mmbiz.qpic.cn/tampered"
		}
		r := serveContentAPI(h, cookie, "POST", base+"apply", wechatJSON(t, map[string]any{"articles": articles, "receipts": initial.Receipts}), headers)
		if r.Code != 409 || len(reads) != 0 {
			t.Fatal("metadata tamper was accepted", field, r.Code, reads)
		}
	}
	created := apply(map[string]any{"articles": initial.Articles, "receipts": initial.Receipts})
	modernID := created.Items[0].PostID
	secondArticle, err := wechat.ParseArticle("https://mp.weixin.qq.com/s/second", sources["/s/second"])
	if err != nil {
		t.Fatal(err)
	}
	second := wechat.Batch([]wechat.Collected{secondArticle}, nil, owner.UserID, owner.Label(), owner.Username)
	plan, err := s.repository.PreviewImport(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := s.repository.ApplyImport(t.Context(), second, plan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	before := []content.Post{}
	for _, id := range []int64{modernID, secondPlan.Items[0].PostID} {
		p, err := s.repository.GetByID(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		p, err = s.repository.Update(t.Context(), id, content.Mutation{ExpectedVersion: p.Version}, content.PostInput{Type: p.Type, Status: p.Status, Slug: p.Slug, Title: "人工内容", Excerpt: "人工摘要", ContentSource: "<p>人工正文</p>", AuthorID: owner.UserID})
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, p)
	}
	sources["/s/modern"] = markup("第一篇导入", "只有摘要更新", "modern")
	sources["/s/second"] = markup("第二篇导入", "原摘要", "second-changed")
	mixed := preview("/s/modern", "/s/second", "/s/freshx")
	if mixed.Plan.New != 1 || mixed.Plan.Existing != 2 || mixed.Plan.Items[0].Action != "changed" || mixed.Plan.Items[1].Action != "changed" || len(reads) != 1 {
		t.Fatal(mixed.Plan, reads)
	}
	// Existing rows must never cause remote media work when imported together
	// with new articles using their individual receipts.
	result := apply(map[string]any{"articles": mixed.Articles, "receipts": mixed.Receipts})
	if result.New != 1 || result.Existing != 2 || result.Items[1].Action != "changed" || !reflect.DeepEqual(reads, []string{"/modern", "/freshx"}) {
		t.Fatal(result, reads)
	}
	apply(map[string]any{"articles": mixed.Articles, "receipts": mixed.Receipts})
	if !reflect.DeepEqual(reads, []string{"/modern", "/freshx"}) {
		t.Fatal("retry downloaded existing images", reads)
	}
	for _, p := range before {
		after, err := s.repository.GetByID(t.Context(), p.ID)
		if err != nil || !reflect.DeepEqual(p, after) {
			t.Fatal("mixed import overwrote manual edits", after, err)
		}
	}
	sources["/s/modern"] = markup("第一篇导入", "原摘要", "cover-only-change")
	if plan := preview("/s/modern").Plan; plan.Items[0].Action != "changed" || !strings.Contains(plan.Items[0].Message, "摘要或封面") {
		t.Fatal("cover-only change missed", plan)
	}
}
