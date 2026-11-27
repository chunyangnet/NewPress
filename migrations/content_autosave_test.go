package migrations

import (
	"strings"
	"testing"
)

func TestContentAutosaveMigrationContainsSafetyConstraints(t *testing.T) {
	source, err := Files.ReadFile("0008_content_autosave.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, fragment := range []string{
		"PRIMARY KEY (post_id, owner_key)",
		"REFERENCES np_posts(id) ON DELETE CASCADE",
		"source_version > 0",
		"jsonb_typeof(snapshot) = 'object'",
		"expires_at > updated_at",
		"np_post_autosaves_expires_idx",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("autosave migration missing %q", fragment)
		}
	}
}
