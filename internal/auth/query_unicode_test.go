package auth

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestUserSearchRejectsMalformedUTF8(t *testing.T) {
	for _, mode := range []string{"normalize", "volatile", "file"} {
		t.Run(mode, func(t *testing.T) {
			var store *FileUserStore
			if mode != "normalize" {
				filename := ""
				if mode == "file" {
					filename = filepath.Join(t.TempDir(), "users.json")
				}
				var err error
				store, err = NewFileUserStore(filename)
				if err != nil {
					t.Fatal(err)
				}
			}
			for name, search := range map[string]string{"bad_byte": "\xff", "truncated": "\xe4\xb8", "surrogate": "\xed\xa0\x80", "embedded": "before\xffafter"} {
				for _, public := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/public_%v", name, public), func(t *testing.T) {
						query := UserQuery{Search: " \u3000" + search + "\t", PublicOnly: public}
						var err error
						if store == nil {
							_, err = NormalizeUserQuery(query)
						} else {
							_, err = store.ListUsers(t.Context(), query)
						}
						if UserFieldErrors(err)["search"] == "" {
							t.Fatalf("missing search validation for %q: %v", search, err)
						}
					})
				}
			}
		})
	}
}

func TestUserSearchPreservesUnicodeAcrossReopen(t *testing.T) {
	hash, err := HashPassword("Unicode search account password 2026")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"volatile", "file"} {
		t.Run(mode, func(t *testing.T) {
			filename := ""
			if mode == "file" {
				filename = filepath.Join(t.TempDir(), "users.json")
			}
			store, err := NewFileUserStore(filename)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.EnsureRoles(t.Context(), BuiltInRoles()); err != nil {
				t.Fatal(err)
			}
			user, err := store.CreateUser(t.Context(), UserMutation{}, UserInput{
				Username: "unicode-reader", Email: "private-reader@example.test", DisplayName: "查询 中文 🌐 �",
				Status: UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"subscriber"},
			}, hash)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "file" {
				store, err = NewFileUserStore(filename)
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, needle := range []string{"中文", "🌐", "�"} {
				for _, public := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/public_%v", needle, public), func(t *testing.T) {
						query, err := NormalizeUserQuery(UserQuery{Search: " \u3000" + needle + "\t", PublicOnly: public})
						if err != nil || query.Search != needle {
							t.Fatalf("Unicode normalization changed search: %#v, %v", query, err)
						}
						result, err := store.ListUsers(t.Context(), query)
						if err != nil || result.Total != 1 || len(result.Items) != 1 || result.Items[0].ID != user.ID {
							t.Fatalf("Unicode user search lost its match: total=%d err=%v", result.Total, err)
						}
					})
				}
			}
		})
	}
}
