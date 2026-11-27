package backup

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/comment"
)

func TestBackupRestoresCommentSubmissionReceipts(t *testing.T) {
	fixture := fixtureSite(t, true)
	ledger, err := audit.Open(filepath.Join(fixture.cfg.DataDir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	store, err := comment.NewFileStore(filepath.Join(fixture.cfg.DataDir, "comments.json"), false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	requests := []comment.Submission{
		{Key: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), Input: comment.CreateInput{PostID: fixture.post.ID, AuthorName: "Reader", AuthorEmail: "reader@example.test", ContentSource: "Preserve receipt", Status: comment.StatusApproved}},
		{Key: strings.Repeat("c", 64), Fingerprint: strings.Repeat("d", 64), Input: comment.CreateInput{PostID: fixture.post.ID, AuthorName: "Reader", AuthorEmail: "reader@example.test", ContentSource: "Deleted comment", Status: comment.StatusApproved}},
	}
	ids := []int64{}
	for _, request := range requests {
		result, err := store.CreateOnce(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, result.Receipt.CommentID)
	}
	if err := store.Delete(t.Context(), ids[1], comment.Mutation{ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(fixture.parent, "comment-receipts.zip")
	if _, err := Create(t.Context(), fixture.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(fixture.parent, "restored-comments"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	store, err = comment.NewFileStore(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "comments.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	for i, request := range requests {
		result, err := store.CreateOnce(t.Context(), request)
		if err != nil || !result.Replayed || result.Receipt.CommentID != ids[i] {
			t.Fatal(result, err)
		}
	}
	if _, err := store.Get(t.Context(), ids[1]); !errors.Is(err, comment.ErrNotFound) {
		t.Fatal("deleted comment returned after restore", err)
	}
	if next, err := store.Create(t.Context(), requests[0].Input); err != nil || next.ID <= ids[1] {
		t.Fatal(next, err)
	}
}
