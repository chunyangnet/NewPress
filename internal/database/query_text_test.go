package database

import (
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestPostgresListQueryRejectsInvalidTextBeforeStorage(t *testing.T) {
	for _, kind := range []string{"admin", "terms", "series", "redirects"} {
		for name, search := range map[string]string{"bad_byte": "\xff", "NUL": "before\x00after"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Errorf("invalid query reached database access: %v", recovered)
					}
				}()
				repository := NewPostRepository(nil)
				var err error
				switch kind {
				case "admin":
					_, err = repository.ListAdmin(t.Context(), content.AdminQuery{Search: search})
				case "terms":
					_, err = repository.ListTerms(t.Context(), content.TermQuery{Search: search})
				case "series":
					_, err = repository.ListSeries(t.Context(), content.SeriesQuery{Search: search})
				case "redirects":
					_, err = repository.ListRedirects(t.Context(), content.RedirectQuery{Search: search})
				}
				if err == nil {
					t.Fatalf("accepted invalid search %q", search)
				}
			})
		}
	}
}
