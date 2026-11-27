package mediausage

import (
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestEditorialSubmissionPreservesMediaGuardsAndReplay(t *testing.T) {
	raw := content.NewMemoryRepository(false)
	s, item := newUsageTestService(t, raw)
	request := content.Submission{Key: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), Action: "create", Input: content.PostInput{Title: "引用图片", CoverURL: item.PublicURL}}
	created, err := s.Content().Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an unavailable object after a confirmed write. Replay reads the
	// receipt without treating its old input as a new media reference.
	if _, err := s.library.Trash(item.ID, item.Version); err != nil {
		t.Fatal(err)
	}
	if result, err := s.Content().Submit(t.Context(), request); err != nil || !result.Replayed {
		t.Fatal(result, err)
	}
	request.Key = strings.Repeat("c", 64)
	if _, err := s.Content().Submit(t.Context(), request); len(content.FieldErrors(err)) == 0 {
		t.Fatal("missing media accepted", err)
	}
	request.Action = "save"
	request.PostID = created.Receipt.PostID
	request.Version = 1
	if _, err := s.Content().Submit(t.Context(), request); len(content.FieldErrors(err)) == 0 {
		t.Fatal("missing update media accepted", err)
	}
	request.Input.CoverURL = ""
	if _, err := s.Content().Submit(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	revisions, _ := raw.ListRevisions(t.Context(), created.Receipt.PostID, 20)
	request.Key = strings.Repeat("d", 64)
	request.Action = "restore_revision"
	request.Version = 2
	request.RevisionID = revisions[len(revisions)-1].ID
	if _, err := s.Content().Submit(t.Context(), request); len(content.FieldErrors(err)) == 0 {
		t.Fatal("restore bypassed media guard", err)
	}
}
