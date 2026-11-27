package content

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestImplicitTermSlugCollisionsSurviveRestart(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, prefix := range []string{strings.Repeat("类", 54), strings.Repeat("类", 53) + "a"} {
			t.Run(fmt.Sprintf("%s/%d-byte-prefix", mode, len(prefix)), func(t *testing.T) {
				ctx := t.Context()
				var repository Repository = NewMemoryRepository(false)
				path := filepath.Join(t.TempDir(), "content.json")
				if mode == "file" {
					var err error
					repository, err = NewFileRepository(path, false)
					if err != nil {
						t.Fatal(err)
					}
				}
				categoryIDs, tagIDs := make(map[int64]bool), make(map[int64]bool)
				for i := range 12 {
					// Distinct valid names share the same truncated slug; exercise
					// suffixes that grow from one digit to two digits as well.
					name := fmt.Sprintf("%s%d", prefix, i)
					post, err := repository.Create(ctx, PostInput{
						Title: "分类碰撞", Slug: fmt.Sprintf("collision-%d", i),
						Category: name, TagNames: []string{name},
					})
					if err != nil {
						t.Fatal(err)
					}
					if categoryIDs[post.Categories[0].ID] || tagIDs[post.Tags[0].ID] {
						t.Fatal("distinct names unexpectedly reused a term")
					}
					categoryIDs[post.Categories[0].ID], tagIDs[post.Tags[0].ID] = true, true
					if mode == "file" {
						repository, err = NewFileRepository(path, false)
						if err != nil {
							t.Fatalf("reload after collision %d: %v", i, err)
						}
					}
					for _, term := range []Term{post.Categories[0], post.Tags[0]} {
						if !utf8.ValidString(term.Slug) || !ValidTermSlug(term.Slug) {
							t.Fatalf("invalid %s slug (%d bytes): %q", term.Taxonomy, len(term.Slug), term.Slug)
						}
						loaded, err := repository.GetTermByID(ctx, term.ID)
						if err != nil || loaded.Name != name || loaded.Slug != term.Slug {
							t.Fatalf("term after reload = %#v, %v", loaded, err)
						}
					}
					// Reusing the full original name must not allocate a new term.
					reused, err := repository.Create(ctx, PostInput{
						Title: "复用分类", Slug: fmt.Sprintf("reused-%d", i),
						Category: name, TagNames: []string{name},
					})
					if err != nil || reused.Categories[0].ID != post.Categories[0].ID || reused.Tags[0].ID != post.Tags[0].ID {
						t.Fatalf("reused terms = %#v, %v", reused, err)
					}
				}
			})
		}
	}
}
