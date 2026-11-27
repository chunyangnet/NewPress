package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contentimport"
	"newpress.local/newpress/internal/media"
)

func TestContentImportRejectsInvalidMediaMappings(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 4, 3)), media.UploadInput{Filename: "cover.png"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	input := contentimport.Request{
		Source: "media-map", Format: "markdown", State: "draft",
		Files: []contentimport.File{{Name: "article.md", Text: "---\nslug: mapped-media\ncover: https://old.test/cover.png\n---\n![Cover](https://old.test/cover.png)"}},
		Media: map[string]string{},
	}
	before, err := s.repository.ListAdmin(t.Context(), content.AdminQuery{})
	if err != nil {
		t.Fatal(err)
	}
	encode := func(token string) string {
		t.Helper()
		data, err := json.Marshal(map[string]any{"input": input, "token": token})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	headers := authenticatedAPIHeaders(owner)
	for _, target := range []string{
		"/media/photo.png",
		"/media/0/cover.png",
		"/media/01/cover.png",
		"/media/not-an-id/cover.png",
		item.PublicURL + "/extra",
		item.PublicURL + "/",
		strings.Replace(item.PublicURL, "/media/", "/media//", 1),
		"/media/999/missing.png",
		strings.Replace(item.PublicURL, "cover.png", "wrong.png", 1),
	} {
		t.Run(target, func(t *testing.T) {
			input.Media["https://old.test/cover.png"] = target
			response := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/admin/imports/preview", encode(""), headers)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("invalid media mapping returned %d: %s", response.Code, response.Body.String())
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if _, exists := result["token"]; exists {
				t.Fatal("invalid media mapping received an apply token")
			}
		})
	}
	after, err := s.repository.ListAdmin(t.Context(), content.AdminQuery{})
	if err != nil || after.Total != before.Total {
		t.Fatalf("rejected preview changed content: %d -> %d, %v", before.Total, after.Total, err)
	}
	input.Media["https://old.test/cover.png"] = item.PublicURL
	preview := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/admin/imports/preview", encode(""), headers)
	var plan importResponse
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil || preview.Code != http.StatusOK || plan.Token == "" {
		t.Fatalf("valid preview failed: %d %s, %v", preview.Code, preview.Body.String(), err)
	}
	apply := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/admin/imports/apply", encode(plan.Token), headers)
	var applied importResponse
	if err := json.Unmarshal(apply.Body.Bytes(), &applied); err != nil || apply.Code != http.StatusOK || !applied.Applied {
		t.Fatalf("valid apply failed: %d %s, %v", apply.Code, apply.Body.String(), err)
	}
	post, err := s.repository.GetByID(t.Context(), applied.Plan.Items[0].PostID)
	if err != nil || post.CoverURL != item.PublicURL || !strings.Contains(post.ContentHTML, `src="`+item.PublicURL+`"`) {
		t.Fatalf("mapped media not saved: %+v, %v", post, err)
	}
	image := serveContentAPI(handler, nil, http.MethodGet, "https://example.test"+item.PublicURL, "", nil)
	if image.Code != http.StatusOK || image.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("mapped image could not be read: %d", image.Code)
	}
}
