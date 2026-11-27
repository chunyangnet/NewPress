package comment

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestCommentSearchRejectsMalformedUTF8(t *testing.T) {
	for _, mode := range []string{"normalize", "volatile", "file"} {
		t.Run(mode, func(t *testing.T) {
			var store *FileStore
			if mode != "normalize" {
				filename := ""
				if mode == "file" {
					filename = filepath.Join(t.TempDir(), "comments.json")
				}
				var err error
				store, err = NewFileStore(filename, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			for name, search := range map[string]string{"bad_byte": "\xff", "truncated": "\xe4\xb8", "surrogate": "\xed\xa0\x80", "embedded": "before\xffafter"} {
				for _, own := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/own_%v", name, own), func(t *testing.T) {
						query := Query{Search: " \u3000" + search + "\t", OwnOnly: own, UserID: 1}
						var err error
						if store == nil {
							_, err = NormalizeQuery(query)
						} else {
							_, err = store.ListAdmin(t.Context(), query)
						}
						if !errors.Is(err, ErrInvalidInput) {
							t.Fatalf("missing search validation for %q: %v", search, err)
						}
					})
				}
			}
		})
	}
}

func TestCommentSearchPreservesUnicodeAcrossReopen(t *testing.T) {
	for _, mode := range []string{"volatile", "file"} {
		t.Run(mode, func(t *testing.T) {
			filename := ""
			if mode == "file" {
				filename = filepath.Join(t.TempDir(), "comments.json")
			}
			store, err := NewFileStore(filename, false)
			if err != nil {
				t.Fatal(err)
			}
			item, err := store.Create(t.Context(), CreateInput{
				PostID: 1, UserID: 1, AuthorName: "读者", AuthorEmail: "private-reader@example.test",
				Status: StatusApproved, ContentSource: "查询 中文 🌐 �",
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "file" {
				store, err = NewFileStore(filename, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, needle := range []string{"中文", "🌐", "�"} {
				for _, own := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/own_%v", needle, own), func(t *testing.T) {
						query, err := NormalizeQuery(Query{Search: " \u3000" + needle + "\t", OwnOnly: own, UserID: 1})
						if err != nil || query.Search != needle {
							t.Fatalf("Unicode normalization changed search: %#v, %v", query, err)
						}
						result, err := store.ListAdmin(t.Context(), query)
						if err != nil || result.Total != 1 || len(result.Items) != 1 || result.Items[0].ID != item.ID {
							t.Fatalf("Unicode comment search lost its match: total=%d err=%v", result.Total, err)
						}
					})
				}
			}
		})
	}
}
