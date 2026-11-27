package database

import (
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestPostgresPublishedQueryRejectsSplitUTF8BeforeStorage(t *testing.T) {
	for name, query := range map[string]content.PublishedQuery{
		"search_category": {Search: "\xc2", Category: "\xa9"},
		"search_author":   {Search: "\xe4\xb8", LegacyAuthor: "\xad"},
		"category_author": {Category: "\xf0\x9f", LegacyAuthor: "\x8c\x90"},
		"three_fields":    {Search: "\xe4", Category: "\xb8", LegacyAuthor: "\xad"},
	} {
		t.Run(name, func(t *testing.T) {
			// No connection is needed: these invalid queries must return before
			// the repository tries to begin a transaction with its absent pool.
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("invalid query reached database access: %v", recovered)
				}
			}()
			if _, err := NewPostRepository(nil).SearchPublished(t.Context(), query); err == nil {
				t.Fatal("invalid query was accepted")
			}
		})
	}
}
