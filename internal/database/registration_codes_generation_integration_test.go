package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/auth"
)

func TestPostgreSQLRegistrationCodeGenerationAndReimport(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := pgx.Identifier{fmt.Sprintf("registration_generation_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config := admin.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	otherPool, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	repository, other := NewUserRepository(pool), NewUserRepository(otherPool)
	now := time.Now().UTC().Truncate(time.Second)
	seen := make(map[string]bool, auth.MaxRegistrationCodes)
	var original auth.RegistrationCode
	for offset := 0; offset < 4500; offset += auth.MaxRegistrationCodeBatch {
		codes := make([]string, auth.MaxRegistrationCodeBatch)
		for i := range codes {
			codes[i] = fmt.Sprintf("%06d", offset+i)
			seen[codes[i]] = true
		}
		created, err := repository.MutateRegistrationCodes(ctx, auth.RegistrationCodeMutation{Action: "create", Codes: codes, Rules: auth.RegistrationCodeRules{Enabled: true, MaxUses: 1}}, "test", now)
		if err != nil {
			t.Fatal(err)
		}
		if offset == 0 {
			original = created[0]
		}
	}
	type result struct {
		items []auth.RegistrationCode
		err   error
	}
	results := make(chan result, 10)
	start := make(chan struct{})
	for i := range 10 {
		store := repository
		if i%2 == 1 {
			store = other
		}
		go func() {
			<-start
			items, err := store.MutateRegistrationCodes(ctx, auth.RegistrationCodeMutation{Action: "generate", Generator: auth.RegistrationCodeGenerator{Count: 50, Length: 6, Charset: "digits"}, Rules: auth.RegistrationCodeRules{Enabled: true, MaxUses: 3, Note: "generated"}}, "test", now)
			results <- result{items, err}
		}()
	}
	close(start)
	for range 10 {
		result := <-results
		if result.err != nil || len(result.items) != 50 {
			t.Errorf("generation failed to fill a concurrent batch: count=%d err=%v", len(result.items), result.err)
			continue
		}
		for _, code := range result.items {
			if len(code.Code) != 6 || seen[code.Code] || code.MaxUses != 3 || code.Note != "generated" || code.Version != 1 {
				t.Errorf("invalid generated record: %+v", code)
			}
			seen[code.Code] = true
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	stored, err := other.ListRegistrationCodes(ctx)
	if err != nil || len(stored) != auth.MaxRegistrationCodes || len(seen) != auth.MaxRegistrationCodes {
		t.Fatal("generated inventory was not committed", len(stored), err)
	}
	_, err = repository.MutateRegistrationCodes(ctx, auth.RegistrationCodeMutation{Action: "generate", Generator: auth.RegistrationCodeGenerator{Count: 1, Length: 6, Charset: "digits"}}, "test", now)
	if !errors.Is(err, auth.ErrRegistrationCodeLimit) {
		t.Fatal("generation ignored the inventory limit", err)
	}
	stale := []auth.RegistrationCodeTarget{{ID: original.ID, Version: original.Version}}
	if _, err := repository.MutateRegistrationCodes(ctx, auth.RegistrationCodeMutation{Action: "delete", Targets: stale}, "test", now); err != nil {
		t.Fatal(err)
	}
	reimported, err := other.MutateRegistrationCodes(ctx, auth.RegistrationCodeMutation{Action: "create", Codes: []string{original.Code}, Rules: auth.RegistrationCodeRules{Enabled: true, MaxUses: 1}}, "test", now)
	if err != nil {
		t.Fatal(err)
	}
	if reimported[0].ID == original.ID || reimported[0].Version != 1 {
		t.Fatal("reimport reused the deleted registration record's identity")
	}
	note := "stale editor"
	for _, action := range []string{"update", "delete"} {
		_, err := repository.MutateRegistrationCodes(ctx, auth.RegistrationCodeMutation{Action: action, Targets: stale, Patch: auth.RegistrationCodePatch{Note: &note}}, "test", now)
		if !errors.Is(err, auth.ErrRegistrationCodeConflict) {
			t.Fatal("deleted record target affected the replacement", action, err)
		}
	}
	stored, err = other.ListRegistrationCodes(ctx)
	if err != nil || len(stored) != auth.MaxRegistrationCodes {
		t.Fatal("failed mutation changed the inventory size", len(stored), err)
	}
	found := false
	for _, code := range stored {
		if !seen[code.Code] {
			t.Fatal("unexpected stored registration code")
		}
		if code.ID == reimported[0].ID {
			found = true
			if code.Note != "" || code.Version != 1 {
				t.Fatal("stale mutation changed the replacement")
			}
		}
	}
	if !found {
		t.Fatal("stale deletion removed the replacement")
	}
}
