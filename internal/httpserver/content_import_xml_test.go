package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contentimport"
)

func TestContentImportRejectsAmbiguousWXR(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	const source = `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:wp="http://wordpress.org/export/1.2/" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><wp:wxr_version>1.2</wp:wxr_version><item><title>Protected import</title><wp:post_id>51</wp:post_id><wp:post_type>post</wp:post_type><wp:post_name>wxr-protected</wp:post_name><wp:status>publish</wp:status><wp:post_password>source-password</wp:post_password><content:encoded><![CDATA[<p>Private source content</p>]]></content:encoded></item></channel></rss>`
	input := contentimport.Request{Source: "wxr-structure", Format: "wordpress", State: "preserve", Files: []contentimport.File{{Name: "export.xml", Text: source}}}
	encode := func(token string) string {
		t.Helper()
		data, err := json.Marshal(map[string]any{"input": input, "token": token})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	headers := authenticatedAPIHeaders(owner)
	before, err := s.repository.ListAdmin(t.Context(), content.AdminQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, xml string }{
		{"multiple roots", source + strings.TrimPrefix(source, `<?xml version="1.0" encoding="UTF-8"?>`)},
		{"trailing data", source + "unparsed trailing data"},
		{"duplicate version", strings.Replace(source, "<wp:wxr_version>1.2</wp:wxr_version>", "<wp:wxr_version>1.1</wp:wxr_version><wp:wxr_version>1.2</wp:wxr_version>", 1)},
		{"foreign item", strings.Replace(strings.Replace(source, "<item>", `<x:item xmlns:x="urn:other">`, 1), "</item>", "</x:item>", 1)},
		{"foreign title", strings.Replace(source, "</title>", `</title><x:title xmlns:x="urn:other">Wrong title</x:title>`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			input.Files[0].Text = test.xml
			response := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/admin/imports/preview", encode(""), headers)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("invalid WXR preview returned %d: %s", response.Code, response.Body.String())
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if _, ok := result["token"]; ok {
				t.Fatal("invalid WXR received an apply token")
			}
		})
	}
	after, err := s.repository.ListAdmin(t.Context(), content.AdminQuery{})
	if err != nil || after.Total != before.Total {
		t.Fatalf("rejected previews changed content: before=%d after=%d err=%v", before.Total, after.Total, err)
	}

	// Rejection must not poison the next preview, and a valid password-protected
	// export still imports with its existing private-content contract.
	input.Files[0].Text = source
	previewResponse := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/admin/imports/preview", encode(""), headers)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("valid WXR preview returned %d: %s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview importResponse
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Token == "" || preview.Plan.New != 1 || preview.Plan.Items[0].Status != content.StatusPrivate {
		t.Fatalf("invalid preview plan: %+v", preview.Plan)
	}
	applyResponse := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/admin/imports/apply", encode(preview.Token), headers)
	if applyResponse.Code != http.StatusOK {
		t.Fatalf("valid WXR apply returned %d: %s", applyResponse.Code, applyResponse.Body.String())
	}
	var applied importResponse
	if err := json.Unmarshal(applyResponse.Body.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || len(applied.Plan.Items) != 1 || applied.Plan.Items[0].Action != "created" {
		t.Fatalf("valid WXR was not imported: %+v", applied.Plan)
	}
	post, err := s.repository.GetByID(t.Context(), applied.Plan.Items[0].PostID)
	if err != nil || post.Status != content.StatusPrivate || !strings.Contains(post.ContentHTML, "Private source content") {
		t.Fatalf("protected content was not retained: %+v, %v", post, err)
	}
	publicResponse := serveContentAPI(handler, nil, http.MethodGet, "https://example.test/posts/wxr-protected", "", nil)
	if publicResponse.Code != http.StatusNotFound {
		t.Fatalf("imported private content was exposed: %d", publicResponse.Code)
	}
}
