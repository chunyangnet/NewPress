package database

import (
	"os"
	"strings"
	"testing"
)

func TestBulkCommentModerationKeepsPostgreSQLAtomicContract(t *testing.T) {
	source, err := os.ReadFile("comments.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "func (repository *CommentRepository) BulkChangeStatus")
	end := strings.Index(text, "func (repository *CommentRepository) Delete")
	if start < 0 || end <= start {
		t.Fatal("could not isolate CommentRepository.BulkChangeStatus")
	}
	body := text[start:end]
	markers := []string{
		"comment.NormalizeBulkStatusChange(change)",
		"repository.pool.Begin(ctx)",
		"defer tx.Rollback(ctx)",
		"ORDER BY id",
		"FOR UPDATE",
		"len(locked) != len(ids)",
		"item.Version != expectedVersions[item.ID]",
		"UPDATE np_comments",
		"INSERT INTO np_audit_events",
		"tx.Commit(ctx)",
	}
	position := -1
	for _, marker := range markers {
		next := strings.Index(body, marker)
		if next < 0 {
			t.Fatalf("BulkChangeStatus lost transaction marker %q", marker)
		}
		if next <= position {
			t.Fatalf("BulkChangeStatus transaction marker %q is out of order", marker)
		}
		position = next
	}
	for _, forbidden := range []string{"repository.pool.Exec", "repository.pool.Query", "repository.pool.QueryRow", "repository.ChangeStatus("} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("BulkChangeStatus escaped or fragmented its transaction through %q", forbidden)
		}
	}
	if strings.Count(body, "WHERE id=ANY($1::bigint[])") < 2 || !strings.Contains(body, "version=version+1") {
		t.Fatal("BulkChangeStatus lost its bounded set update, reload or version increment")
	}
}
