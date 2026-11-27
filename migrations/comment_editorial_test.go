package migrations

import (
	"strings"
	"testing"
)

func TestCommentEditorialMigrationKeepsRevisionAndAuditBoundaries(t *testing.T) {
	source, err := Files.ReadFile("0009_comment_editorial.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, fragment := range []string{
		"staff_reply boolean NOT NULL DEFAULT false",
		"created_by varchar(160) NOT NULL DEFAULT ''",
		"edited_by varchar(160) NOT NULL DEFAULT ''",
		"edited_at timestamptz",
		"CREATE TABLE IF NOT EXISTS np_comment_revisions",
		"REFERENCES np_comments(id) ON DELETE CASCADE",
		"PRIMARY KEY (comment_id, version)",
		"np_comment_revisions_content_bounded",
		"np_comments_edit_metadata_valid",
		"np_comments_staff_reply_metadata_valid",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("comment editorial migration missing %q", fragment)
		}
	}
	if strings.Contains(strings.ToLower(text), "author_ip") || strings.Contains(strings.ToLower(text), "user_agent") {
		t.Fatal("comment revisions must not duplicate request-source metadata")
	}
}
