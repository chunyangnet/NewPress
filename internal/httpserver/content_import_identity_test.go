package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contentimport"
)

func TestContentImportAlignsLinksBySourceIdentity(t *testing.T) {
	for _, format := range []string{"markdown", "wordpress"} {
		t.Run(format, func(t *testing.T) {
			s, handler, users, _, cookie, owner := newPersistentAdminTestServer(t)
			mappedAuthor, _, _ := createAndLoginTestUser(t, users, owner, "mapped-import-author", "mapped-import@example.test", "Mapped author", "author")
			input := contentimport.Request{Source: "identity", Format: format, State: "draft", Authors: map[string]int64{"": mappedAuthor.ID}}
			if format == "markdown" {
				input.Files = []contentimport.File{{Name: "old.md", Text: "---\nslug: shared\n---\nOld source"}}
			} else {
				input.Files = []contentimport.File{{Name: "export.xml", Text: `<rss xmlns:wp="http://wordpress.org/export/1.2/" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><wp:wxr_version>1.2</wp:wxr_version><item><title>Old source</title><link>https://old.test/old/</link><wp:post_id>1</wp:post_id><wp:post_type>post</wp:post_type><wp:post_name>shared</wp:post_name><wp:status>draft</wp:status><content:encoded>Old source</content:encoded></item></channel></rss>`}}
			}
			request := func(action, token string) importResponse {
				t.Helper()
				body, err := json.Marshal(map[string]any{"input": input, "token": token})
				if err != nil {
					t.Fatal(err)
				}
				response := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/admin/imports/"+action, string(body), authenticatedAPIHeaders(owner))
				if response.Code != http.StatusOK {
					t.Fatalf("%s returned %d: %s", action, response.Code, response.Body.String())
				}
				var result importResponse
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			preview := request("preview", "")
			if preview.Token == "" {
				t.Fatalf("no preview token: %+v", preview.Plan)
			}
			applied := request("apply", preview.Token)
			oldID := applied.Plan.Items[0].PostID
			old, err := s.repository.Update(t.Context(), oldID, content.Mutation{ExpectedVersion: applied.Plan.Items[0].Version}, content.PostInput{
				Title: "Manually edited old content", Slug: "renamed", Status: content.StatusDraft, ContentSource: "<p>Saved edit</p>", AuthorID: owner.UserID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if format == "markdown" {
				input.Files = append(input.Files,
					contentimport.File{Name: "new.md", Text: "---\nslug: shared\n---\nNew source"},
					contentimport.File{Name: "index.md", Text: "---\nslug: index\n---\n[Old](old.md#one)\n\n[New](new.md#two)\n\n[Direct](/posts/shared#three)"},
				)
			} else {
				input.Files[0].Text = strings.Replace(input.Files[0].Text, "</channel>", `<item><title>New source</title><link>https://old.test/new/</link><wp:post_id>2</wp:post_id><wp:post_type>post</wp:post_type><wp:post_name>shared</wp:post_name><wp:status>draft</wp:status><content:encoded>New source</content:encoded></item><item><title>Index</title><link>https://old.test/index/</link><wp:post_id>3</wp:post_id><wp:post_type>post</wp:post_type><wp:post_name>index</wp:post_name><wp:status>draft</wp:status><content:encoded><![CDATA[<a href="https://old.test/old/#one">Old</a><a href="https://old.test/new/#two">New</a><a href="/posts/shared#three">Direct</a>]]></content:encoded></item></channel>`, 1)
			}
			checkLinks := func(body string) {
				t.Helper()
				want := []string{"/posts/renamed#one", "/posts/shared#two", "/posts/shared#three"}
				if format == "wordpress" {
					want[2] = "https://old.test/posts/shared#three"
				}
				for _, href := range want {
					if !strings.Contains(body, fmt.Sprintf(`href="%s"`, href)) {
						t.Errorf("missing link %q: %s", href, body)
					}
				}
			}
			preview = request("preview", "")
			if preview.Token == "" || preview.Plan.New != 2 || preview.Plan.Existing != 1 || preview.Plan.Conflicts != 0 {
				t.Fatalf("wrong mixed-batch preview: %+v", preview.Plan)
			}
			checkLinks(preview.Document.Items[2].Preview)
			applied = request("apply", preview.Token)
			if !applied.Applied || applied.Plan.Items[0].Action != "skip" || applied.Plan.Items[1].Action != "created" || applied.Plan.Items[2].Action != "created" {
				t.Fatalf("wrong mixed-batch result: %+v", applied.Plan)
			}
			checkLinks(applied.Document.Items[2].Preview)
			index, err := s.repository.GetByID(t.Context(), applied.Plan.Items[2].PostID)
			if err != nil {
				t.Fatal(err)
			}
			checkLinks(index.ContentHTML)
			if index.AuthorID != mappedAuthor.ID || index.AuthorName != mappedAuthor.DisplayName {
				t.Fatalf("mapped author lost during alignment: %+v", index)
			}
			retained, err := s.repository.GetByID(t.Context(), oldID)
			if err != nil || retained.Version != old.Version || retained.Slug != old.Slug || retained.ContentHTML != old.ContentHTML || retained.AuthorID != old.AuthorID {
				t.Fatalf("reimport changed the existing article: %+v, %v", retained, err)
			}
		})
	}
}
