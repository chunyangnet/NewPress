package database

import (
	"os"
	"strings"
	"testing"
)

func TestAuditReaderSQLProjectsOnlySafeColumns(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("audit.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	selectStart := strings.Index(text, "SELECT id, actor_label, action, object_type, object_id, created_at")
	if selectStart < 0 {
		t.Fatal("audit query safe column list is missing")
	}
	selectEnd := strings.Index(text[selectStart:], "FROM np_audit_events")
	if selectEnd < 0 {
		t.Fatal("audit query FROM clause is missing")
	}
	projection := text[selectStart : selectStart+selectEnd]
	for _, forbidden := range []string{"metadata", "request_id", "actor_user_id", "password", "email", "content", "user_agent", "token", "session"} {
		if strings.Contains(strings.ToLower(projection), forbidden) {
			t.Fatalf("audit SQL projection contains forbidden column %q: %s", forbidden, projection)
		}
	}
	for _, required := range []string{"ORDER BY created_at DESC, id DESC", "LIMIT $"} {
		if !strings.Contains(text, required) {
			t.Fatalf("audit SQL missing bounded cursor contract %q", required)
		}
	}
}
