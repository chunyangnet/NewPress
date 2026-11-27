package database

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
)

func TestIdentitySearchRejectsMalformedUTF8BeforeDatabase(t *testing.T) {
	for _, kind := range []string{"users", "comments"} {
		for name, search := range map[string]string{"bad_byte": "\xff", "truncated": "\xe4\xb8", "surrogate": "\xed\xa0\x80", "embedded": "before\xffafter"} {
			for _, scoped := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/scoped_%v", kind, name, scoped), func(t *testing.T) {
					// An inert pool distinguishes input validation from nil-repository
					// checks. These invalid searches must never invoke the pool.
					defer func() {
						if recovered := recover(); recovered != nil {
							t.Errorf("invalid query reached database access: %v", recovered)
						}
					}()
					pool := &pgxpool.Pool{}
					if kind == "users" {
						_, err := NewUserRepository(pool).ListUsers(t.Context(), auth.UserQuery{Search: search, PublicOnly: scoped})
						if auth.UserFieldErrors(err)["search"] == "" {
							t.Fatalf("missing user search validation: %v", err)
						}
					} else {
						_, err := NewCommentRepository(pool).ListAdmin(t.Context(), comment.Query{Search: search, OwnOnly: scoped, UserID: 1})
						if !errors.Is(err, comment.ErrInvalidInput) {
							t.Fatalf("missing comment search validation: %v", err)
						}
					}
				})
			}
		}
	}
}
