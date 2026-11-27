package content

import (
	"errors"
	"testing"
	"time"
)

func TestNormalizeBulkStatusChangeBoundsPairsAndSorts(t *testing.T) {
	change, fields := NormalizeBulkStatusChange(BulkStatusChange{
		Status: " PUBLISH ", Actor: " editor ",
		Items: []BulkMutation{{ID: 9, ExpectedVersion: 3}, {ID: 2, ExpectedVersion: 1}},
	})
	if len(fields) != 0 || change.Status != StatusPublish || change.Actor != "editor" ||
		len(change.Items) != 2 || change.Items[0].ID != 2 || change.Items[1].ID != 9 {
		t.Fatalf("normalized bulk change = %#v, fields=%#v", change, fields)
	}

	for name, candidate := range map[string]BulkStatusChange{
		"empty":     {Status: StatusDraft},
		"duplicate": {Status: StatusDraft, Items: []BulkMutation{{ID: 1, ExpectedVersion: 1}, {ID: 1, ExpectedVersion: 2}}},
		"invalid":   {Status: StatusDraft, Items: []BulkMutation{{ID: 0, ExpectedVersion: 1}}},
		"status":    {Status: "execute", Items: []BulkMutation{{ID: 1, ExpectedVersion: 1}}},
		"scope":     {Status: StatusDraft, AuthorID: -1, Items: []BulkMutation{{ID: 1, ExpectedVersion: 1}}},
	} {
		if _, candidateFields := NormalizeBulkStatusChange(candidate); len(candidateFields) == 0 {
			t.Fatalf("%s bulk change unexpectedly passed", name)
		}
	}
	tooMany := BulkStatusChange{Status: StatusDraft, Items: make([]BulkMutation, MaxBulkMutations+1)}
	for index := range tooMany.Items {
		tooMany.Items[index] = BulkMutation{ID: int64(index + 1), ExpectedVersion: 1}
	}
	if _, candidateFields := NormalizeBulkStatusChange(tooMany); candidateFields["selection"] == "" {
		t.Fatalf("too many bulk items passed: %#v", candidateFields)
	}
}

func TestMemoryBulkChangeStatusIsAtomicVersionedAndClearsAutosaves(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository(false)
	repository.now = func() time.Time { return now }
	create := func(slug, title string) Post {
		post, err := repository.Create(t.Context(), PostInput{
			Type: TypePost, Status: StatusDraft, Slug: slug, Title: title, ContentSource: "正文",
			AuthorID: 7, AuthorName: "作者", Actor: "author", Category: "未分类", CommentStatus: CommentOpen,
		})
		if err != nil {
			t.Fatal(err)
		}
		return post
	}
	first := create("bulk-first", "批量一")
	second := create("bulk-second", "批量二")
	for index, post := range []Post{first, second} {
		snapshot := post
		snapshot.Title += "恢复点"
		if _, err := repository.SaveAutosave(t.Context(), Autosave{
			PostID: post.ID, OwnerKey: "user:" + string(rune('1'+index)), SourceVersion: post.Version,
			Snapshot: snapshot, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	firstRevisions, _ := repository.ListRevisions(t.Context(), first.ID, 20)
	secondRevisions, _ := repository.ListRevisions(t.Context(), second.ID, 20)

	_, err := repository.BulkChangeStatus(t.Context(), BulkStatusChange{
		Status: StatusPublish, Actor: "editor",
		Items: []BulkMutation{{ID: second.ID, ExpectedVersion: second.Version + 1}, {ID: first.ID, ExpectedVersion: first.Version}},
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale bulk error = %v", err)
	}
	for _, original := range []Post{first, second} {
		current, _ := repository.GetByID(t.Context(), original.ID)
		if current.Status != original.Status || current.Version != original.Version {
			t.Fatalf("stale bulk partially mutated post: %#v", current)
		}
	}
	if _, err := repository.GetAutosave(t.Context(), first.ID, "user:1"); err != nil {
		t.Fatalf("stale bulk cleared first autosave: %v", err)
	}
	if _, err := repository.GetAutosave(t.Context(), second.ID, "user:2"); err != nil {
		t.Fatalf("stale bulk cleared second autosave: %v", err)
	}
	if revisions, _ := repository.ListRevisions(t.Context(), first.ID, 20); len(revisions) != len(firstRevisions) {
		t.Fatalf("stale bulk recorded first revision: %d", len(revisions))
	}
	if revisions, _ := repository.ListRevisions(t.Context(), second.ID, 20); len(revisions) != len(secondRevisions) {
		t.Fatalf("stale bulk recorded second revision: %d", len(revisions))
	}

	_, err = repository.BulkChangeStatus(t.Context(), BulkStatusChange{
		Status: StatusTrash, Actor: "editor",
		Items: []BulkMutation{{ID: first.ID, ExpectedVersion: first.Version}, {ID: 999999, ExpectedVersion: 1}},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing bulk error = %v", err)
	}
	currentFirst, _ := repository.GetByID(t.Context(), first.ID)
	if currentFirst.Version != first.Version || currentFirst.Status != first.Status {
		t.Fatalf("missing bulk partially mutated first post: %#v", currentFirst)
	}
	_, err = repository.BulkChangeStatus(t.Context(), BulkStatusChange{
		Status: StatusTrash, Actor: "editor", AuthorID: 99,
		Items: []BulkMutation{{ID: first.ID, ExpectedVersion: first.Version}, {ID: second.ID, ExpectedVersion: second.Version}},
	})
	if !errors.Is(err, ErrAuthorScope) {
		t.Fatalf("author-scoped bulk error = %v", err)
	}
	currentFirst, _ = repository.GetByID(t.Context(), first.ID)
	if currentFirst.Version != first.Version || currentFirst.Status != first.Status {
		t.Fatalf("author-scoped bulk partially mutated first post: %#v", currentFirst)
	}

	now = now.Add(5 * time.Minute)
	updated, err := repository.BulkChangeStatus(t.Context(), BulkStatusChange{
		Status: StatusPublish, Actor: "editor",
		Items: []BulkMutation{{ID: second.ID, ExpectedVersion: second.Version}, {ID: first.ID, ExpectedVersion: first.Version}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 2 || updated[0].ID != first.ID || updated[1].ID != second.ID {
		t.Fatalf("bulk result order = %#v", updated)
	}
	for _, post := range updated {
		if post.Status != StatusPublish || post.Version != 2 || !post.PublishedAt.Equal(now) || !post.UpdatedAt.Equal(now) {
			t.Fatalf("bulk updated post = %#v", post)
		}
	}
	for index, post := range []Post{first, second} {
		owner := "user:" + string(rune('1'+index))
		if _, err := repository.GetAutosave(t.Context(), post.ID, owner); !errors.Is(err, ErrAutosaveNotFound) {
			t.Fatalf("bulk write did not clear %s: %v", owner, err)
		}
		revisions, listErr := repository.ListRevisions(t.Context(), post.ID, 20)
		if listErr != nil || len(revisions) != 2 || revisions[0].Version != 2 || revisions[0].Actor != "editor" || revisions[0].Snapshot.Status != StatusPublish {
			t.Fatalf("bulk revisions for %d = %#v, err=%v", post.ID, revisions, listErr)
		}
	}
	updated[0].Meta["category"] = "caller mutation"
	reloaded, _ := repository.GetByID(t.Context(), first.ID)
	if reloaded.Meta["category"] == "caller mutation" {
		t.Fatal("bulk result was not cloned")
	}
}
