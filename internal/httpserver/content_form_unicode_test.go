package httpserver

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestContentFormsRejectMalformedUTF8WithoutWritesOrReceipts(t *testing.T) {
	s, handler, _, cookie, principal := newAuthenticatedContentServer(t)
	for _, route := range []string{"classic", "editor"} {
		for _, operation := range []string{"create", "update"} {
			for _, field := range []string{"title", "excerpt", "content_source", "cover_url", "category", "tags", "slug"} {
				t.Run(route+"/"+operation+"/"+field, func(t *testing.T) {
					filename := filepath.Join(t.TempDir(), "content.json")
					repository, err := content.NewFileRepository(filename, false)
					if err != nil {
						t.Fatal(err)
					}
					s.repository = repository
					form := url.Values{
						"csrf_token": {principal.CSRFToken}, "action": {"save"}, "type": {content.TypePost},
						"status": {content.StatusDraft}, "title": {"合法😀�"}, "slug": {"original"},
						"excerpt": {"摘要"}, "content_source": {"<p>正文</p>"}, "cover_url": {"/media/cover.png"},
						"category": {"分类"}, "tags": {"标签"}, "comment_status": {content.CommentOpen},
						"published_timezone": {"Asia/Shanghai"}, "published_source": {"canonical"},
					}
					target := "/admin/content/new/"
					if operation == "update" {
						original, err := repository.Create(t.Context(), content.PostInput{Title: "原文", Slug: "original"})
						if err != nil {
							t.Fatal(err)
						}
						target = fmt.Sprintf("/admin/content/%d/edit/", original.ID)
						form.Set("version", strconv.FormatInt(original.Version, 10))
					}
					before, err := os.ReadFile(filename)
					if err != nil {
						t.Fatal(err)
					}
					valid := cloneValues(form)
					form.Set(field, "/bad-\xff")
					key := "encoding-" + operation + "-" + field
					var response *httptest.ResponseRecorder
					if route == "editor" {
						// The editor route uses a form-encoded body and an atomic receipt.
						response = editorSubmit(handler, cookie, target, key, form, nil)
					} else {
						response = serveContentForm(handler, cookie, "https://example.test"+target, "https://example.test", form)
					}
					if response.Code != http.StatusUnprocessableEntity {
						t.Errorf("malformed percent-encoded field accepted: %d", response.Code)
					}
					if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(before, after) {
						t.Errorf("invalid form changed content, terms, revisions or receipts: %v", err)
					}
					if route == "editor" {
						retry := editorSubmit(handler, cookie, target, key, valid, nil)
						if retry.Code != http.StatusOK {
							t.Errorf("invalid input consumed receipt or version: %d %s", retry.Code, retry.Body.String())
						}
					}
				})
			}
		}
	}
}
