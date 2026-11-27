package httpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/content"
)

func TestArticleComponentCodeValidationReturnsPublishableConfig(t *testing.T) {
	_, handler, _, repo, cookie, owner := newPersistentAdminTestServer(t)
	c := testComponent("code")
	c.Text = strings.Repeat("<div>&</div>\n", 1800)
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(c); err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(raw.Bytes())
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/article-components/validate",
		currencyJSON(t, map[string]string{"config": input}), authenticatedAPIHeaders(owner))
	if response.Code != http.StatusOK {
		t.Fatalf("validation: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Config string `json:"config"`
	}
	decodeAPIResponse(t, response, &body)
	validated, err := articlewidgets.Decode(body.Config)
	if err != nil || validated.Text != c.Text {
		t.Fatalf("validation returned unusable configuration: %v", err)
	}
	post := createWidgetPost(t, repo, content.PostInput{
		AuthorID: owner.UserID, Title: "HTML 代码示例", Slug: "code-config-round-trip", Status: content.StatusPublish,
		ContentSource: `<aside data-np-widget="code" data-np-config="` + body.Config + `"><p>代码示例</p></aside>`,
	})
	page := serveContentAPI(handler, nil, "GET", "https://example.test/posts/"+post.Slug, "", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `data-np-widget="code"`) || !strings.Contains(page.Body.String(), "&lt;div&gt;&amp;&lt;/div&gt;") {
		t.Fatalf("validated code component did not render safely: HTTP %d", page.Code)
	}
}
