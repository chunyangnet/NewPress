package content

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTextPatchPreservesUnspecifiedFields(t *testing.T) {
	current := Post{ID: 3, AuthorID: 7, Type: TypeFlash, Status: StatusFuture, Slug: "keep-slug", Title: "original",
		Excerpt: " original excerpt ", ContentSource: " <custom>raw body</custom> ", ContentHTML: " <em>saved</em> ",
		PublishedAt: time.Now().Add(-time.Hour), Meta: map[string]any{"resources": "preserve"}, Version: 18}
	title := "  new title  "
	next, changed, err := (TextPatch{Title: &title}).Apply(current)
	want := current
	want.Title = "new title"
	if err != nil || !changed || !reflect.DeepEqual(next, want) {
		t.Fatal("partial write changed omitted fields", next, err)
	}
	if _, changed, err = (TextPatch{Title: &title}).Apply(next); err != nil || changed {
		t.Fatal("identical patch must be a no-op", err)
	}
	empty := ""
	next, _, err = (TextPatch{Excerpt: &empty}).Apply(current)
	if err != nil || next.Excerpt != "" || next.ContentSource != current.ContentSource {
		t.Fatal("empty excerpt generated text", err)
	}
	body := `<p>safe</p><script>alert(1)</script><img src="javascript:alert(2)" onerror="alert(3)">`
	next, _, err = (TextPatch{ContentSource: &body}).Apply(current)
	if err != nil || strings.Contains(next.ContentHTML, "<script") || strings.Contains(next.ContentHTML, "onerror") || strings.Contains(next.ContentHTML, "javascript:") || next.Excerpt != current.Excerpt {
		t.Fatal("body sanitizer or preservation failed", next, err)
	}
}

func TestTextPatchRejectsInvalidInputs(t *testing.T) {
	empty, control, long, invalid := " ", "a\x00b", strings.Repeat("中", MaxTitleRunes+1), string([]byte{0xff})
	for _, patch := range []TextPatch{{}, {Title: &empty}, {Title: &control}, {Title: &long}, {ContentSource: &invalid}} {
		if _, _, err := patch.Apply(Post{}); err == nil {
			t.Fatal("invalid patch accepted")
		}
	}
	tooLongFlash := strings.Repeat("中", 5001)
	for _, source := range []string{"", tooLongFlash} {
		if _, _, err := (TextPatch{ContentSource: &source}).Apply(Post{Type: TypeFlash}); err == nil {
			t.Fatal("flash body rules were bypassed")
		}
	}
}
