package database

import (
	"errors"
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestPostgreSQLFlashRedirectPathsAndOwnership(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := NewPostRepository(pool)
	input := content.PostInput{Type: content.TypeFlash, Title: "Flash", Slug: "old-flash", ContentSource: "Flash body", Status: content.StatusPublish}
	post, err := repo.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := repo.SaveRedirect(ctx, 0, content.Mutation{}, content.RedirectInput{Path: "/legacy-flash/", PostID: post.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repo.ResolveRedirect(ctx, manual.Path); err != nil || got != content.PublicPath(post) {
		t.Errorf("flash alias points to the wrong content route: %q %v", got, err)
	}
	if rule, err := repo.SaveRedirect(ctx, 0, content.Mutation{}, content.RedirectInput{Path: content.PublicPath(post), PostID: post.ID}); !errors.Is(err, content.ErrRedirectConflict) {
		t.Errorf("canonical flash URL accepted as a redirect: %+v %v", rule, err)
		if err == nil {
			if err := repo.DeleteRedirect(ctx, rule.ID, content.Mutation{ExpectedVersion: rule.Version}); err != nil {
				t.Fatal(err)
			}
		}
	}
	oldPath := content.PublicPath(post)
	input.Slug = "new-flash"
	post, err = repo.Update(ctx, post.ID, content.Mutation{ExpectedVersion: post.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{manual.Path, oldPath} {
		if got, err := repo.ResolveRedirect(ctx, path); err != nil || got != content.PublicPath(post) {
			t.Errorf("renamed flash alias %s = %q, %v", path, got, err)
		}
	}
	input.Slug, input.Status = "old-flash", content.StatusPrivate
	if _, err := repo.Create(ctx, input); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.ResolveRedirect(ctx, oldPath); !errors.Is(err, content.ErrNotFound) {
		t.Errorf("private canonical flash URL exposed another redirect target: %q, %v", got, err)
	}
	if _, err := repo.ChangeStatus(ctx, post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusDraft); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.ResolveRedirect(ctx, manual.Path); !errors.Is(err, content.ErrNotFound) {
		t.Errorf("draft target remained visible: %q, %v", got, err)
	}
}
