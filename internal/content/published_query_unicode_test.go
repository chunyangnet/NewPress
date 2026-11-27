package content

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishedQueryRejectsInvalidUTF8Fields(t *testing.T) {
	type testCase struct {
		name  string
		query PublishedQuery
	}
	var cases []testCase
	// Split real two-, three- and four-byte characters at every byte boundary.
	// Their concatenation is valid, but the two independent fields are not.
	for _, character := range []string{"©", "中", "🌐"} {
		for split := 1; split < len(character); split++ {
			for boundary, pair := range [][2]int{{0, 1}, {0, 2}, {1, 2}} {
				var fields [3]string
				fields[pair[0]], fields[pair[1]] = character[:split], character[split:]
				cases = append(cases, testCase{
					name:  fmt.Sprintf("%d_byte_split_%d/pair_%d", len(character), split, boundary),
					query: PublishedQuery{Search: fields[0], Category: fields[1], LegacyAuthor: fields[2]},
				})
			}
		}
	}
	cases = append(cases, testCase{"three_fields", PublishedQuery{Search: "\xe4", Category: "\xb8", LegacyAuthor: "\xad"}})
	for _, invalid := range []struct{ name, value string }{{"invalid_byte", "\xff"}, {"control", "before\x00after"}} {
		for field := range 3 {
			var fields [3]string
			fields[field] = invalid.value
			cases = append(cases, testCase{
				name:  fmt.Sprintf("%s/field_%d", invalid.name, field),
				query: PublishedQuery{Search: fields[0], Category: fields[1], LegacyAuthor: fields[2]},
			})
		}
	}
	for _, mode := range []string{"normalize", "memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			var repository Repository
			if mode == "memory" {
				repository = NewMemoryRepository(false)
			} else if mode == "file" {
				var err error
				repository, err = NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					var err error
					if repository == nil {
						_, err = NormalizePublishedQuery(test.query)
					} else {
						_, err = repository.SearchPublished(t.Context(), test.query)
					}
					if err == nil {
						t.Fatalf("accepted invalid independent fields: %#v", test.query)
					}
				})
			}
		})
	}
}

func TestPublishedQueryPreservesUnicodeFilters(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			repository, _, reopen := unicodeWriteRepository(t, mode)
			const category, author = "编码指南 🌐 �", "创作者 🌐 �"
			post, err := repository.Create(t.Context(), PostInput{
				Title: "中文 🌐 �", Slug: "unicode-query", Category: category, AuthorName: author,
				Status: StatusPublish, PublishedAt: time.Now().Add(-time.Hour),
			})
			if err != nil {
				t.Fatal(err)
			}
			repository = reopen()
			for _, needle := range []string{"中文", "🌐", "�"} {
				t.Run(needle, func(t *testing.T) {
					query, err := NormalizePublishedQuery(PublishedQuery{
						Search: " \u3000" + needle + "\t", Category: " " + category + " ", LegacyAuthor: "\n" + author + "\n",
					})
					if err != nil || query.Search != needle || query.Category != category || query.LegacyAuthor != author {
						t.Fatalf("valid Unicode filters changed: %#v, %v", query, err)
					}
					result, err := repository.SearchPublished(t.Context(), query)
					if err != nil || result.Total != 1 || len(result.Items) != 1 || result.Items[0].ID != post.ID {
						t.Fatalf("Unicode filters lost their matching post: %#v, %v", result, err)
					}
				})
			}
		})
	}
}
