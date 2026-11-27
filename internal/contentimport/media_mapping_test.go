package contentimport

import (
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestImportRejectsMalformedMediaMappingTargets(t *testing.T) {
	for _, target := range []string{
		"/media/photo.png",
		"/media/0/photo.png",
		"/media/-1/photo.png",
		"/media/01/photo.png",
		"/media/+1/photo.png",
		"/media/not-an-id/photo.png",
		"/media/9223372036854775808/photo.png",
		"/media/1/",
		"/media/1/photo.png/nested",
		"/media/1/photo.png/",
		"/media//1/photo.png",
		"/media/./1/photo.png",
		"/media/1/%2e/photo.png",
	} {
		t.Run(target, func(t *testing.T) {
			_, err := Parse(Request{
				Source: "media-map", Format: "markdown", State: "draft",
				Files: []File{{Name: "article.md", Text: "![Cover](https://old.test/cover.png)"}},
				Media: map[string]string{"https://old.test/cover.png": target},
			}, time.Now())
			if content.FieldErrors(err)["import"] == "" {
				t.Fatalf("malformed media mapping %q accepted: %v", target, err)
			}
		})
	}
}

func TestImportAcceptsManagedMediaMappingTargets(t *testing.T) {
	for _, target := range []string{
		"/media/1/photo.png",
		"/media/123/%E5%9B%BE.png",
		"/media/123/图.png",
		"/media/1/a%2520b.png",
		"/media/1/photo.png?download=1#preview",
		"/media/1/photo.png?variant=display&format=webp",
	} {
		t.Run(target, func(t *testing.T) {
			doc, err := Parse(Request{
				Source: "media-map", Format: "markdown", State: "draft",
				Files: []File{{Name: "article.md", Text: "---\ncover: https://old.test/cover.png\n---\n![Cover](https://old.test/cover.png)"}},
				Media: map[string]string{"https://old.test/cover.png": target},
			}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Items[0].Images) != 0 || !strings.Contains(doc.Items[0].Preview, "<img") || doc.Batch.Items[0].Input.CoverURL != target {
				t.Fatalf("valid mapping was lost: %+v", doc.Items[0])
			}
		})
	}
}
