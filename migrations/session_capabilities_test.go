package migrations

import (
	"strings"
	"testing"
)

func TestSessionCapabilityMigrationKeepsEditorAndAuthorIdentityScoped(t *testing.T) {
	source, err := Files.ReadFile("0005_session_capabilities.sql")
	if err != nil {
		t.Fatalf("read session capability migration: %v", err)
	}

	sql := string(source)
	for _, role := range []string{"editor", "author"} {
		if !strings.Contains(sql, "WHERE slug = '"+role+"'") {
			t.Errorf("migration does not update %s", role)
		}
	}
	for _, capability := range []string{"sessions:read", "sessions:manage:own"} {
		if strings.Count(sql, `"`+capability+`"`) != 2 {
			t.Errorf("migration should grant %s once to each identity-scoped role", capability)
		}
	}
	if strings.Contains(sql, `"sessions:manage"`) {
		t.Fatal("migration must not grant global session management to editor or author")
	}
}
