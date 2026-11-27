package content

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryAutosaveIsScopedClonedExpiringAndNonCanonical(t *testing.T) {
	now := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository(false)
	repository.now = func() time.Time { return now }
	post, err := repository.Create(t.Context(), PostInput{
		Type: TypePost, Status: StatusDraft, Slug: "autosave-boundary", Title: "正式标题",
		ContentSource: "<p>正式正文</p>", AuthorName: "Editor", Actor: "editor",
		Category: "未分类", CommentStatus: CommentOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	revisionsBefore, _ := repository.ListRevisions(t.Context(), post.ID, 20)
	snapshot := post
	snapshot.Title = "恢复点标题"
	snapshot.ContentSource = `<p>恢复点正文</p><script>never()</script>`
	snapshot.ContentHTML = "<p>恢复点正文</p>"
	snapshot.Meta = map[string]any{"nested": map[string]any{"labels": []any{"one", "two"}}}
	saved, err := repository.SaveAutosave(t.Context(), Autosave{
		PostID: post.ID, OwnerKey: "user:42", SourceVersion: post.Version,
		Snapshot: snapshot, ExpiresAt: now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := repository.GetByID(t.Context(), post.ID)
	revisionsAfter, _ := repository.ListRevisions(t.Context(), post.ID, 20)
	if canonical.Title != post.Title || canonical.Version != post.Version || !canonical.UpdatedAt.Equal(post.UpdatedAt) || len(revisionsAfter) != len(revisionsBefore) {
		t.Fatalf("autosave mutated canonical content: canonical=%#v revisions=%d/%d", canonical, len(revisionsAfter), len(revisionsBefore))
	}

	loaded, err := repository.GetAutosave(t.Context(), post.ID, "user:42")
	if err != nil || loaded.Snapshot.Title != "恢复点标题" || !loaded.UpdatedAt.Equal(saved.UpdatedAt) {
		t.Fatalf("loaded autosave=%#v err=%v", loaded, err)
	}
	loaded.Snapshot.Title = "mutated caller"
	loaded.Snapshot.Meta["nested"].(map[string]any)["labels"].([]any)[0] = "mutated"
	again, _ := repository.GetAutosave(t.Context(), post.ID, "user:42")
	if again.Snapshot.Title != "恢复点标题" || again.Snapshot.Meta["nested"].(map[string]any)["labels"].([]any)[0] != "one" {
		t.Fatalf("autosave was not deeply cloned: %#v", again)
	}
	if _, err := repository.GetAutosave(t.Context(), post.ID, "user:43"); !errors.Is(err, ErrAutosaveNotFound) {
		t.Fatalf("identity scope error = %v", err)
	}

	now = now.Add(25 * time.Hour)
	if _, err := repository.GetAutosave(t.Context(), post.ID, "user:42"); !errors.Is(err, ErrAutosaveNotFound) {
		t.Fatalf("expired autosave error = %v", err)
	}
}

func TestMemoryAutosaveUsesOptimisticVersionAndCanonicalWritesClearAllOwners(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository(false)
	repository.now = func() time.Time { return now }
	post, err := repository.Create(t.Context(), PostInput{
		Type: TypePage, Status: StatusDraft, Slug: "safety-net", Title: "Safety Net",
		ContentSource: "First", AuthorName: "Editor", Actor: "editor", CommentStatus: CommentClosed,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"user:1", "legacy:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		snapshot := post
		snapshot.Title = owner
		if _, err := repository.SaveAutosave(t.Context(), Autosave{
			PostID: post.ID, OwnerKey: owner, SourceVersion: post.Version,
			Snapshot: snapshot, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	stale := post
	stale.Version++
	if _, err := repository.SaveAutosave(t.Context(), Autosave{
		PostID: post.ID, OwnerKey: "user:2", SourceVersion: stale.Version,
		Snapshot: stale, ExpiresAt: now.Add(time.Hour),
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale autosave error = %v", err)
	}

	input := PostInput{
		Type: TypePage, Status: StatusDraft, Slug: post.Slug, Title: "Canonical v2",
		ContentSource: "Second", AuthorID: post.AuthorID, AuthorName: post.AuthorName,
		Actor: "editor", CommentStatus: CommentClosed,
	}
	if _, err := repository.Update(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version, Actor: "editor"}, input); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"user:1", "legacy:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		if _, err := repository.GetAutosave(t.Context(), post.ID, owner); !errors.Is(err, ErrAutosaveNotFound) {
			t.Fatalf("canonical save did not clear %s: %v", owner, err)
		}
	}
}
