package migrations

import (
	"strings"
	"testing"
)

func TestCommentRuntimeMigrationDefinesModerationAndPrivacyBoundaries(t *testing.T) {
	source, err := Files.ReadFile("0006_comment_runtime.sql")
	if err != nil {
		t.Fatalf("read comment runtime migration: %v", err)
	}
	sql := string(source)
	for _, fragment := range []string{
		"version bigint NOT NULL DEFAULT 1",
		"author_ip_hash char(64) NOT NULL DEFAULT ''",
		"user_agent varchar(512) NOT NULL DEFAULT ''",
		"status IN ('pending', 'approved', 'spam', 'trash')",
		"octet_length(content_source) BETWEEN 1 AND 12288",
		"np_comments_status_updated_idx",
		"np_comments_post_parent_created_idx",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("comment migration missing %q", fragment)
		}
	}
	if strings.Contains(sql, "author_ip varchar") || strings.Contains(sql, "author_ip inet") {
		t.Fatal("comment migration must persist only a keyed source digest, not raw IP addresses")
	}
}
