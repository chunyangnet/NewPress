package migrations

import (
	"strings"
	"testing"
)

func TestAuditQueryMigrationAddsBoundedReadIndexes(t *testing.T) {
	t.Parallel()
	source, err := Files.ReadFile("0010_audit_query.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"np_audit_events_action_created_idx",
		"(action, created_at DESC, id DESC)",
		"np_audit_events_type_created_idx",
		"(object_type, created_at DESC, id DESC)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("audit query migration missing %q", required)
		}
	}
}
