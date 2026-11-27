package comment

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/audit"
)

func TestFileCommentStoreWritesRedactedAuditEvents(t *testing.T) {
	directory := t.TempDir()
	ledgerPath := filepath.Join(directory, "audit.jsonl")
	ledger, err := audit.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(filepath.Join(directory, "comments.json"), false, ledger)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(context.Background(), CreateInput{
		PostID: 1, AuthorName: "Private Author", AuthorEmail: "private-comment@example.test",
		ContentSource: "private comment body sentinel", AuthorIPHash: strings.Repeat("a", 64), UserAgent: "private-user-agent-sentinel",
	})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := store.Edit(context.Background(), created.ID, Mutation{ExpectedVersion: created.Version, Actor: "editor"}, EditInput{
		AuthorName: "Edited Private Author", AuthorEmail: "edited-private@example.test", ContentSource: "edited private body sentinel",
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.RestoreRevision(context.Background(), created.ID, created.Version, Mutation{ExpectedVersion: edited.Version, Actor: "restorer"})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := store.ChangeStatus(context.Background(), created.ID, Mutation{ExpectedVersion: restored.Version, Actor: "moderator"}, StatusApproved)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := store.Reply(context.Background(), ReplyInput{
		ParentID: approved.ID, ExpectedParentVersion: approved.Version, UserID: 7, AuthorName: "Staff Private",
		AuthorEmail: "staff-private@example.test", ContentSource: "staff private body sentinel", Actor: "staff-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := store.BulkChangeStatus(context.Background(), BulkStatusChange{
		Status: StatusSpam, Actor: "bulk-moderator", Items: []BulkMutation{
			{ID: approved.ID, ExpectedVersion: approved.Version}, {ID: reply.ID, ExpectedVersion: reply.Version},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := changed[0]
	root, err = store.ChangeStatus(context.Background(), root.ID, Mutation{ExpectedVersion: root.Version, Actor: "moderator"}, StatusTrash)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), root.ID, Mutation{ExpectedVersion: root.Version, Actor: "moderator"}); err != nil {
		t.Fatal(err)
	}
	actions := make(map[string]bool)
	for _, record := range ledger.Records() {
		if record.Phase == "commit" {
			actions[record.Action] = true
		}
	}
	for _, action := range []string{
		"comment.created", "comment.edit", "comment.revision.restore", "comment.status.approved",
		"comment.reply", "comment.status.spam", "comment.status.trash", "comment.delete",
	} {
		if !actions[action] {
			t.Fatalf("missing committed action %s in %#v", action, actions)
		}
	}
	payload, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"Private Author", "private-comment@example.test", "private comment body sentinel", strings.Repeat("a", 64), "private-user-agent-sentinel",
		"Edited Private Author", "edited-private@example.test", "edited private body sentinel",
		"Staff Private", "staff-private@example.test", "staff private body sentinel",
	} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("audit ledger leaked sensitive comment data %q", secret)
		}
	}
}
