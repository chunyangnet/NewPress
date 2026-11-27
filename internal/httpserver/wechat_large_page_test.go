package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestWeChatCollectionLargePagePreviewAndImport(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	const base = "https://example.test/api/v1/admin/wechat/collect/"
	const address = "https://mp.weixin.qq.com/s/large1"
	source := `<script>` + strings.Repeat("/* page runtime */", 200000) + `</script><meta property="og:title" content="大页面文章"><span id="js_name">来源公众号</span><div id="js_content"><p>完整的文章正文。</p></div>`
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(source)), Request: req}, nil
	})}
	headers := authenticatedAPIHeaders(owner)
	r := serveContentAPI(handler, cookie, "POST", base+"preview", wechatJSON(t, map[string]any{"urls": []string{address}}), headers)
	var preview partialWeChatPreview
	if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil || r.Code != 200 || len(preview.Articles) != 1 || len(preview.Failures) != 0 || len(preview.Receipts) != 1 || preview.Plan.New != 1 {
		t.Fatalf("large page did not receive a usable preview: status=%d err=%v response=%.1200s", r.Code, err, r.Body.String())
	}
	article := preview.Articles[0]
	if article.Body != "<p>完整的文章正文。</p>" || len(r.Body.Bytes()) > 8192 {
		t.Fatal("page runtime leaked into the preview", len(r.Body.Bytes()))
	}
	r = serveContentAPI(handler, cookie, "POST", base+"apply", wechatJSON(t, map[string]any{"articles": preview.Articles, "receipts": preview.Receipts}), headers)
	var result wechatSelectionPreview
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil || r.Code != 200 || result.Plan.New != 1 || len(result.Plan.Items) != 1 {
		t.Fatalf("large page import failed: status=%d err=%v response=%.1200s", r.Code, err, r.Body.String())
	}
	post, err := s.repository.GetByID(t.Context(), result.Plan.Items[0].PostID)
	if err != nil || post.Status != content.StatusDraft || post.Title != "大页面文章" || !strings.Contains(post.ContentSource, article.Body) || !strings.Contains(post.ContentSource, address) || strings.Contains(post.ContentSource, "page runtime") {
		t.Fatal("import did not preserve clean content and source", err)
	}
}
