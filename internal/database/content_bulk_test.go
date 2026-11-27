package database

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestBulkChangeStatusKeepsPostgreSQLAtomicContract(t *testing.T) {
	source, err := os.ReadFile("posts_write.go")
	if err != nil {
		t.Fatal(err)
	}
	positions := token.NewFileSet()
	parsed, err := parser.ParseFile(positions, "posts_write.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, declaration := range parsed.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "BulkChangeStatus" && function.Recv != nil {
			body = string(source[positions.Position(function.Pos()).Offset:positions.Position(function.End()).Offset])
			break
		}
	}
	if body == "" {
		t.Fatal("could not isolate PostRepository.BulkChangeStatus")
	}
	markers := []string{
		"content.NormalizeBulkStatusChange(change)",
		"r.pool.Begin(ctx)",
		"defer tx.Rollback(ctx)",
		"ORDER BY p.id",
		"FOR UPDATE OF p",
		"len(locked) != len(ids)",
		"post.AuthorID != normalized.AuthorID",
		"post.Version != expectedVersions[post.ID]",
		"UPDATE np_posts",
		"hydratePostTerms(ctx, tx, posts)",
		"insertRevision(ctx, tx, post, normalized.Actor)",
		"DELETE FROM np_post_autosaves",
		"tx.Commit(ctx)",
	}
	position := -1
	for _, marker := range markers {
		relative := strings.Index(body[position+1:], marker)
		if relative < 0 {
			t.Fatalf("BulkChangeStatus lost transaction marker %q", marker)
		}
		next := position + 1 + relative
		if next <= position {
			t.Fatalf("BulkChangeStatus transaction marker %q is out of order", marker)
		}
		position = next
	}
	for _, forbidden := range []string{"r.pool.Exec", "r.pool.Query", "r.pool.QueryRow", "r.ChangeStatus("} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("BulkChangeStatus escaped or fragmented its transaction through %q", forbidden)
		}
	}
	if !strings.Contains(body, "WHERE id=ANY($1::bigint[])") || !strings.Contains(body, "version=version+1") {
		t.Fatal("BulkChangeStatus lost its bounded set update or version increment")
	}
}
