package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contentimport"
)

func TestContentImportPreviewApplyAndSecurity(t *testing.T) {
	s, handler, users, _, cookie, owner := newPersistentAdminTestServer(t)
	_, authorCookie, author := createAndLoginTestUser(t, users, owner, "import-author", "author@example.test", "作者", "author")
	input := contentimport.Request{Source: "my-notes", Format: "markdown", State: "preserve", Files: []contentimport.File{{Name: "article.md", Text: "---\ntitle: 导入文章\nslug: imported-article\nstatus: publish\ndate: 2020-01-02\nauthor: Original\npermalink: /2020/imported/\n---\n# Hello\n\n正文"}}}
	encode := func(input contentimport.Request, token string) string {
		payload, _ := json.Marshal(map[string]any{"input": input, "token": token})
		return string(payload)
	}
	previewURL := "https://example.test/api/v1/admin/imports/preview"
	applyURL := "https://example.test/api/v1/admin/imports/apply"
	for _, test := range []struct {
		cookie  *http.Cookie
		headers map[string]string
		want    int
	}{{nil, nil, 401}, {authorCookie, authenticatedAPIHeaders(author), 403}, {cookie, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"}, 403}} {
		r := serveContentAPI(handler, test.cookie, "POST", previewURL, encode(input, ""), test.headers)
		if r.Code != test.want {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	foreign := authenticatedAPIHeaders(owner)
	foreign["Origin"] = "https://other.test"
	if r := serveContentAPI(handler, cookie, "POST", previewURL, encode(input, ""), foreign); r.Code != 403 {
		t.Fatal(r.Code)
	}
	headers := authenticatedAPIHeaders(owner)
	r := serveContentAPI(handler, cookie, "POST", previewURL, encode(input, ""), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var preview importResponse
	if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Plan.New != 1 || preview.Token == "" || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(preview)
	}
	if _, err := s.repository.GetPublishedBySlug(t.Context(), "imported-article"); err == nil {
		t.Fatal("preview created content")
	}
	changed := input
	changed.Source = "tampered"
	if r := serveContentAPI(handler, cookie, "POST", applyURL, encode(changed, preview.Token), headers); r.Code != 409 {
		t.Fatal("token does not bind input", r.Code)
	}
	expired := s.signImport(importClaims{UserID: owner.UserID, Digest: importRequestDigest(input), Revision: preview.Plan.Revision, Expires: time.Now().Add(-time.Second).Unix()})
	if r := serveContentAPI(handler, cookie, "POST", applyURL, encode(input, expired), headers); r.Code != 409 {
		t.Fatal("expired", r.Code)
	}
	if r := serveContentAPI(handler, cookie, "POST", previewURL, `{"input":{},"input":{}}`, headers); r.Code != 400 {
		t.Fatal("duplicate JSON", r.Code)
	}
	r = serveContentAPI(handler, cookie, "POST", applyURL, encode(input, preview.Token), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var result importResponse
	json.Unmarshal(r.Body.Bytes(), &result)
	if !result.Applied || result.Plan.Items[0].Action != "created" {
		t.Fatal(result)
	}
	post, err := s.repository.GetPublishedBySlug(t.Context(), "imported-article")
	if err != nil || post.AuthorID != owner.UserID {
		t.Fatal(post, err)
	}
	old := serveContentAPI(handler, nil, "GET", "https://example.test/2020/imported/", "", nil)
	if old.Code != 301 {
		t.Fatal(old.Code)
	}
	post, err = s.repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Title: "Edited after migration", Slug: post.Slug, Status: content.StatusPublish, AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(handler, cookie, "POST", applyURL, encode(input, preview.Token), headers)
	if r.Code != 200 {
		t.Fatal("replay", r.Code, r.Body.String())
	}
	current, _ := s.repository.GetByID(t.Context(), post.ID)
	if current.Title != post.Title || current.Version != post.Version {
		t.Fatal("replay overwrote edits")
	}
	input.Source = "bad-media"
	input.Files[0].Text = "---\nslug: missing-import-media\n---\n![Missing](/media/999/missing.png)"
	r = serveContentAPI(handler, cookie, "POST", previewURL, encode(input, ""), headers)
	if r.Code != 422 {
		t.Fatal("media reference not guarded", r.Code, r.Body.String())
	}
	page := serveContentAPI(handler, cookie, "GET", "https://example.test/admin/import/", "", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "data-content-import") {
		t.Fatal(fmt.Sprint(page.Code), page.Body.String())
	}
}
