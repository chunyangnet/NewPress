package content

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestFlashRedirectContractMemoryAndFile(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		name := "memory"
		if persistent {
			name = "file"
		}
		t.Run(name, func(t *testing.T) {
			var repo Repository = NewMemoryRepository(false)
			path := filepath.Join(t.TempDir(), "content.json")
			if persistent {
				var err error
				repo, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			input := PostInput{Type: TypeFlash, Title: "Flash", Slug: "flash-before", ContentSource: "Flash body", Status: StatusPublish}
			post, err := repo.Create(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.SaveRedirect(t.Context(), 0, Mutation{}, RedirectInput{Path: PublicPath(post), PostID: post.ID}); !errors.Is(err, ErrRedirectConflict) {
				t.Fatal("canonical flash path accepted as a redirect", err)
			}
			input.Slug = "flash-after"
			post, err = repo.Update(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version}, input)
			if err != nil {
				t.Fatal(err)
			}
			if persistent {
				repo, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			if got, err := repo.ResolveRedirect(t.Context(), "/flash/flash-before/"); err != nil || got != PublicPath(post) {
				t.Fatal(got, err)
			}
			input.Slug, input.Status = "flash-before", StatusPrivate
			if _, err := repo.Create(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			if got, err := repo.ResolveRedirect(t.Context(), "/flash/flash-before/"); !errors.Is(err, ErrNotFound) {
				t.Fatal("private canonical flash path exposed redirect", got, err)
			}
		})
	}
}
