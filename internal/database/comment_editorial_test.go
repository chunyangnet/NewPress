package database

import (
	"os"
	"strings"
	"testing"
)

func TestCommentEditAndStaffReplyKeepPostgreSQLTransactionContracts(t *testing.T) {
	source, err := os.ReadFile("comments.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	assertTransactionMarkers(t, isolateCommentRepositoryMethod(t, text, "Edit", "RestoreRevision"), []string{
		"comment.NormalizeEditInput(input)",
		"repository.pool.Begin(ctx)",
		"defer tx.Rollback(ctx)",
		"FOR UPDATE",
		"current.Version != mutation.ExpectedVersion",
		"INSERT INTO np_comment_revisions",
		"UPDATE np_comments",
		"INSERT INTO np_audit_events",
		"tx.Commit(ctx)",
	})
	assertTransactionMarkers(t, isolateCommentRepositoryMethod(t, text, "RestoreRevision", "Reply"), []string{
		"comment.NormalizeActor(mutation.Actor)",
		"repository.pool.Begin(ctx)",
		"defer tx.Rollback(ctx)",
		"FOR UPDATE",
		"current.Version != mutation.ExpectedVersion",
		"FROM np_comment_revisions WHERE comment_id=$1 AND version=$2",
		"comment.NormalizeEditInput",
		"INSERT INTO np_comment_revisions",
		"UPDATE np_comments",
		"INSERT INTO np_audit_events",
		"comment.revision.restore",
		"tx.Commit(ctx)",
	})
	assertTransactionMarkers(t, isolateCommentRepositoryMethod(t, text, "Reply", "ListRevisions"), []string{
		"comment.NormalizeReplyInput(input)",
		"repository.pool.Begin(ctx)",
		"defer tx.Rollback(ctx)",
		"FOR UPDATE",
		"parent.Version != normalized.ExpectedParentVersion",
		"validateStaffCommentReplyParent",
		"INSERT INTO np_comments",
		"INSERT INTO np_audit_events",
		"tx.Commit(ctx)",
	})
}

func isolateCommentRepositoryMethod(t *testing.T, source, method, next string) string {
	t.Helper()
	start := strings.Index(source, "func (repository *CommentRepository) "+method)
	end := strings.Index(source, "func (repository *CommentRepository) "+next)
	if start < 0 || end <= start {
		t.Fatalf("could not isolate CommentRepository.%s", method)
	}
	return source[start:end]
}

func assertTransactionMarkers(t *testing.T, body string, markers []string) {
	t.Helper()
	position := -1
	for _, marker := range markers {
		next := strings.Index(body, marker)
		if next < 0 {
			t.Fatalf("transaction lost marker %q", marker)
		}
		if next <= position {
			t.Fatalf("transaction marker %q is out of order", marker)
		}
		position = next
	}
	for _, forbidden := range []string{"repository.pool.Exec", "repository.pool.Query("} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("transaction escaped through %q", forbidden)
		}
	}
}
