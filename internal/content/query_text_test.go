package content

import (
	"fmt"
	"testing"
)

func normalizeListSearch(kind, search string) (string, error) {
	switch kind {
	case "admin":
		query, err := NormalizeAdminQuery(AdminQuery{Search: search})
		return query.Search, err
	case "terms":
		query, err := NormalizeTermQuery(TermQuery{Search: search})
		return query.Search, err
	case "series":
		query, err := NormalizeSeriesQuery(SeriesQuery{Search: search})
		return query.Search, err
	case "redirects":
		query, err := NormalizeRedirectQuery(RedirectQuery{Search: search})
		return query.Search, err
	default:
		return "", fmt.Errorf("unknown query kind: %s", kind)
	}
}

func listTextMatches(t *testing.T, repository Repository, kind, search string) (int, error) {
	t.Helper()
	switch kind {
	case "admin":
		result, err := repository.ListAdmin(t.Context(), AdminQuery{Search: search})
		return result.Total, err
	case "terms":
		result, err := repository.ListTerms(t.Context(), TermQuery{Search: search})
		return result.Total, err
	case "series":
		result, err := repository.ListSeries(t.Context(), SeriesQuery{Search: search})
		return result.Total, err
	case "redirects":
		result, err := repository.ListRedirects(t.Context(), RedirectQuery{Search: search})
		return result.Total, err
	default:
		t.Fatalf("unknown query kind: %s", kind)
		return 0, nil
	}
}

func TestContentListQuerySearchValidation(t *testing.T) {
	for _, kind := range []string{"admin", "terms", "series", "redirects"} {
		for _, test := range []struct {
			name, text string
			valid      bool
		}{
			{"bad_byte", "\xff", false}, {"truncated", "\xe4\xb8", false},
			{"NUL", "before\x00after", false}, {"DEL", "before\x7fafter", false}, {"C1_control", "before\u0085after", false},
			{"Chinese", "中文", true}, {"emoji", "🌐", true}, {"replacement_character", "�", true},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				got, err := normalizeListSearch(kind, " \u3000"+test.text+"\t")
				if (err == nil) != test.valid {
					t.Fatalf("valid=%v err=%v text=%q", test.valid, err, test.text)
				}
				if test.valid && got != test.text {
					t.Fatalf("Unicode changed during trimming: got=%q want=%q", got, test.text)
				}
			})
		}
	}
}

func TestContentListQueriesRejectInvalidTextAtRepository(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			repository, _, _ := unicodeWriteRepository(t, mode)
			for _, kind := range []string{"admin", "terms", "series", "redirects"} {
				for name, search := range map[string]string{"bad_byte": "\xff", "NUL": "before\x00after"} {
					t.Run(kind+"/"+name, func(t *testing.T) {
						if _, err := listTextMatches(t, repository, kind, search); err == nil {
							t.Fatalf("repository accepted invalid search %q", search)
						}
					})
				}
			}
		})
	}
}

func TestContentListQueriesPreserveUnicodeMatches(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			repository, _, reopen := unicodeWriteRepository(t, mode)
			const title = "查询结果 中文 🌐 �"
			post, err := repository.Create(t.Context(), PostInput{Title: title, Slug: "unicode-query-result", Status: StatusPublish})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.CreateTerm(t.Context(), TermInput{Taxonomy: TaxonomyTag, Name: title, Slug: "unicode-query-result"}); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.SaveSeries(t.Context(), 0, Mutation{}, SeriesInput{Kind: SeriesKindTopic, Status: StatusPublish, Title: title, Slug: "unicode-query-result", PostIDs: []int64{post.ID}}); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.SaveRedirect(t.Context(), 0, Mutation{}, RedirectInput{Path: "/legacy/中文/🌐/�/", PostID: post.ID}); err != nil {
				t.Fatal(err)
			}
			repository = reopen()
			for _, kind := range []string{"admin", "terms", "series", "redirects"} {
				for _, needle := range []string{"中文", "🌐", "�"} {
					t.Run(kind+"/"+needle, func(t *testing.T) {
						total, err := listTextMatches(t, repository, kind, " \u3000"+needle+"\t")
						if err != nil || total != 1 {
							t.Fatalf("Unicode search matches=%d err=%v", total, err)
						}
					})
				}
			}
		})
	}
}
