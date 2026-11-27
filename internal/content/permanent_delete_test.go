package content

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPermanentDeleteAtomicAndPersistent(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	submission := Submission{Key: submissionDigest("delete-create"), Fingerprint: submissionDigest("delete-body"), Action: "create", Input: PostInput{Title: "待删除", Slug: "delete-me", AuthorID: 7}}
	created, err := r.Submit(ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.ChangeStatus(ctx, created.Receipt.PostID, Mutation{ExpectedVersion: 1}, StatusTrash)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Create(ctx, PostInput{Title: "保留", Slug: "keep-me", AuthorID: 7})
	if err != nil {
		t.Fatal(err)
	}
	series, err := r.SaveSeries(ctx, 0, Mutation{}, SeriesInput{Kind: SeriesKindTopic, Status: StatusDraft, Title: "专题", Slug: "delete-topic", PostIDs: []int64{first.ID, second.ID}})
	if err != nil {
		t.Fatal(err)
	}
	change := BulkStatusChange{Delete: true, Status: StatusTrash, AuthorID: 7, Items: []BulkMutation{{ID: first.ID, ExpectedVersion: first.Version}, {ID: second.ID, ExpectedVersion: second.Version}}}
	if _, err := r.BulkChangeStatus(ctx, change); len(FieldErrors(err)) == 0 {
		t.Fatalf("active post accepted: %v", err)
	}
	if _, err := r.GetByID(ctx, first.ID); err != nil {
		t.Fatalf("partial deletion: %v", err)
	}
	change.Items = change.Items[:1]
	change.AuthorID = 8
	if _, err := r.BulkChangeStatus(ctx, change); !errors.Is(err, ErrAuthorScope) {
		t.Fatalf("scope: %v", err)
	}
	change.AuthorID = 7
	change.Items[0].ExpectedVersion++
	if _, err := r.BulkChangeStatus(ctx, change); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("version: %v", err)
	}
	change.Items[0].ExpectedVersion--
	if _, err := r.BulkChangeStatus(ctx, change); err != nil {
		t.Fatal(err)
	}
	r, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetByID(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted post persisted: %v", err)
	}
	if _, err := r.ListRevisions(ctx, first.ID, 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revisions survived: %v", err)
	}
	loaded, err := r.GetSeries(ctx, series.ID)
	if err != nil || len(loaded.PostIDs) != 1 || loaded.PostIDs[0] != second.ID || loaded.Version != series.Version+1 {
		t.Fatalf("series: %+v %v", loaded, err)
	}
	replay, err := r.Submit(ctx, submission)
	if err != nil || !replay.Replayed {
		t.Fatalf("creation receipt lost: %+v %v", replay, err)
	}
	if _, err := r.GetByID(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retry recreated post: %v", err)
	}
}
