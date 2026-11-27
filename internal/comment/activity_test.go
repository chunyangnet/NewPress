package comment

import (
	"newpress.local/newpress/internal/audit"
	"path/filepath"
	"testing"
	"time"
)

func TestCommentEventsAndAckShareAuditedSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "comments.json")
	ledger, err := audit.Open(filepath.Join(dir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewFileStore(path, false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Create(t.Context(), CreateInput{PostID: 1, UserID: 2, AuthorName: "读者", AuthorEmail: "reader@example.test", ContentSource: "@other-reader 请看", Status: StatusPending})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeStatus(t.Context(), c.ID, Mutation{ExpectedVersion: c.Version + 1, Actor: "moderator"}, StatusApproved); err == nil {
		t.Fatal("stale mutation accepted")
	}
	events, _ := s.PendingActivity(t.Context(), time.Now(), 50)
	if len(events) != 0 {
		t.Fatal("event escaped failed moderation")
	}
	c, err = s.ChangeStatus(t.Context(), c.ID, Mutation{ExpectedVersion: c.Version, Actor: "moderator"}, StatusApproved)
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewFileStore(path, false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	events, err = s.PendingActivity(t.Context(), time.Now(), 50)
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	for _, event := range events {
		if err = s.AckActivity(t.Context(), event.Key); err != nil {
			t.Fatal(err)
		}
	}
	s, err = NewFileStore(path, false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	events, err = s.PendingActivity(t.Context(), time.Now(), 50)
	if err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
	if err = ledger.Ready(); err != nil {
		t.Fatal("ack broke audit", err)
	}
}
