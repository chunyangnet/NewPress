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

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

func TestWeChatDescriptiveCollectionReceiptAndMediaImport(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	const base = "https://example.test/api/v1/admin/wechat/collect/"
	headers := authenticatedAPIHeaders(owner)
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	imageReads := 0
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		var body io.Reader
		if req.URL.Hostname() == "mp.weixin.qq.com" {
			body = strings.NewReader(`<meta property="og:title" content="图文保真"><div id="js_content"><p id="part">正文</p><a href="#part">原文章节</a><figure><img data-src="https://mmbiz.qpic.cn/picture" alt="细节 &amp; 全貌"><figcaption>图注：真实图片</figcaption></figure></div>`)
		} else if req.URL.Hostname() == "mmbiz.qpic.cn" {
			imageReads++
			body = bytes.NewReader(picture.Bytes())
		} else {
			t.Fatalf("unexpected remote host %s", req.URL.Hostname())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(body), Request: req}, nil
	})}
	r := serveContentAPI(h, cookie, "POST", base+"preview", `{"urls":["https://mp.weixin.qq.com/s/fidelity"]}`, headers)
	var preview struct {
		Articles []wechat.Collected `json:"articles"`
		Receipts map[string]string  `json:"receipts"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil || r.Code != 200 || len(preview.Articles) != 1 {
		t.Fatal(r.Code, r.Body.String(), err)
	}
	a := preview.Articles[0]
	if imageReads != 0 || strings.Contains(a.Preview, "<img") || !strings.Contains(a.Preview, "细节 &amp; 全貌") || !strings.Contains(a.Preview, "图注：真实图片") || !strings.Contains(a.Preview, `href="https://mp.weixin.qq.com/s/fidelity#part"`) {
		t.Fatal("preview fidelity/privacy", a.Preview, imageReads)
	}
	apply := func(articles []wechat.Collected) string {
		return wechatJSON(t, map[string]any{"articles": articles, "receipts": preview.Receipts})
	}
	tampered := append([]wechat.Collected(nil), preview.Articles...)
	tampered[0].Body = strings.ReplaceAll(a.Body, "细节", "篡改描述")
	if got := serveContentAPI(h, cookie, "POST", base+"apply", apply(tampered), headers); got.Code != 409 || imageReads != 0 {
		t.Fatal("altered description bypassed signed preview", got.Code, imageReads)
	}
	r = serveContentAPI(h, cookie, "POST", base+"apply", apply(preview.Articles), headers)
	var result struct {
		Plan content.ImportPlan `json:"plan"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil || r.Code != 200 || len(result.Plan.Items) != 1 {
		t.Fatal(r.Code, r.Body.String(), err)
	}
	post, err := s.repository.GetByID(t.Context(), result.Plan.Items[0].PostID)
	if err != nil || post.Status != content.StatusDraft || imageReads != 1 || !strings.Contains(post.ContentHTML, `alt="细节 &amp; 全貌"`) || !strings.Contains(post.ContentHTML, "图注：真实图片") || !strings.Contains(post.ContentHTML, `href="https://mp.weixin.qq.com/s/fidelity#part"`) {
		t.Fatal(post, err, imageReads)
	}
	if got := serveContentAPI(h, cookie, "POST", base+"apply", apply(preview.Articles), headers); got.Code != 200 || imageReads != 1 {
		t.Fatal("retry downloaded twice", got.Code, imageReads)
	}
}
