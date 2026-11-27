package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegistrationCodeGenerationAndBatchValidation(t *testing.T) {
	for _, charset := range []string{"friendly", "mixed", "digits", "hex"} {
		codes, err := GenerateRegistrationCodes(RegistrationCodeGenerator{Count: 500, Prefix: "NP-", Length: 16, Charset: charset, Group: 4})
		if err != nil || len(codes) != 500 {
			t.Fatal(charset, err)
		}
		seen := map[string]bool{}
		for _, c := range codes {
			if len(c) != 22 || !strings.HasPrefix(c, "NP-") || seen[c] {
				t.Fatal("invalid or duplicate generated code")
			}
			seen[c] = true
		}
	}
	for _, g := range []RegistrationCodeGenerator{{Count: 501, Length: 16, Charset: "friendly"}, {Count: 1, Length: 5, Charset: "digits"}, {Count: 1, Length: 16, Charset: "unknown"}, {Count: 1, Length: 16, Charset: "friendly", Prefix: "<script>"}} {
		if _, err := GenerateRegistrationCodes(g); err == nil {
			t.Fatal("invalid generator accepted", g)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	input := RegistrationCodeMutation{Action: "create", Codes: []string{"Custom-01", "Custom-02"}, Rules: RegistrationCodeRules{Enabled: true, MaxUses: 2}}
	items, _, err := ApplyRegistrationCodeMutation(nil, input, now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ApplyRegistrationCodeMutation(items, RegistrationCodeMutation{Action: "create", Codes: []string{"Fresh-01", "Custom-01"}}, now)
	if !errors.Is(err, ErrRegistrationCodeConflict) || len(items) != 2 {
		t.Fatal("duplicate batch was not atomic", err)
	}
	note := "Updated"
	_, _, err = ApplyRegistrationCodeMutation(items, RegistrationCodeMutation{Action: "update", Targets: []RegistrationCodeTarget{{items[0].ID, 1}, {items[1].ID, 99}}, Patch: RegistrationCodePatch{Note: &note}}, now)
	if !errors.Is(err, ErrRegistrationCodeConflict) || items[0].Note != "" {
		t.Fatal("stale batch partially applied", err)
	}
	used, err := ConsumeRegistrationCode(items, RegistrationCodeHash(items[0].Code), now)
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	updated, _, err := ApplyRegistrationCodeMutation(used, RegistrationCodeMutation{Action: "update", Targets: []RegistrationCodeTarget{{used[0].ID, used[0].Version}}, Patch: RegistrationCodePatch{MaxUses: &zero, Note: &note}}, now)
	if err != nil || updated[0].Used != 1 || updated[0].MaxUses != 0 {
		t.Fatal("usage reset by update", err)
	}
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for _, tc := range []struct {
		name  string
		rules RegistrationCodeRules
		used  int64
		want  string
	}{
		{"active", RegistrationCodeRules{Enabled: true, MaxUses: 1}, 0, "active"},
		{"disabled", RegistrationCodeRules{}, 0, "disabled"},
		{"future", RegistrationCodeRules{Enabled: true, StartsAt: &future}, 0, "scheduled"},
		{"expired", RegistrationCodeRules{Enabled: true, StartsAt: &past, ExpiresAt: &now}, 0, "expired"},
		{"starts now", RegistrationCodeRules{Enabled: true, StartsAt: &now, ExpiresAt: &future}, 0, "active"},
		{"exhausted", RegistrationCodeRules{Enabled: true, MaxUses: 1}, 1, "exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := RegistrationCode{ID: "id", Code: "TEST", RegistrationCodeRules: tc.rules, Used: tc.used}
			if got := RegistrationCodeStatus(c, now); got != tc.want {
				t.Fatal(got)
			}
			_, err := ConsumeRegistrationCode([]RegistrationCode{c}, RegistrationCodeHash("TEST"), now)
			if (err == nil) != (tc.want == "active") {
				t.Fatal("consumption disagrees with status", err)
			}
		})
	}
}

func TestFileRegistrationCodesAtomicConsumptionAndPersistence(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "users.json")
	s, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	codes, err := s.MutateRegistrationCodes(ctx, RegistrationCodeMutation{Action: "create", Codes: []string{"Single-use", "Mail-use", "Fail-save"}, Rules: RegistrationCodeRules{Enabled: true, MaxUses: 1}}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := HashPassword("Registration code test password 2026")
	if err != nil {
		t.Fatal(err)
	}
	newInput := func(name, code string) UserInput {
		return UserInput{Username: name, RoleSlugs: []string{"subscriber"}, RegistrationCodeHash: RegistrationCodeHash(code)}
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := s.CreateUser(ctx, UserMutation{}, newInput(fmt.Sprintf("code-race-%d", i), "Single-use"), hash)
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrRegistrationCodeInvalid) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("code oversubscribed", success)
	}
	count, _ := s.CountManagedUsers(ctx)
	if count != 1 {
		t.Fatal("failed registration left users", count)
	}
	// A conflicting username must not consume an otherwise valid code.
	_, err = s.CreateUser(ctx, UserMutation{}, newInput("mail-conflict", ""), hash)
	if !errors.Is(err, ErrRegistrationCodeInvalid) {
		t.Fatal("invalid proof accepted", err)
	}
	_, err = s.CreateUser(ctx, UserMutation{}, UserInput{Username: "mail-conflict", RoleSlugs: []string{"subscriber"}}, hash)
	if err != nil {
		t.Fatal(err)
	}
	raw, token, err := NewAccessToken(AccessInvite, "codes@example.test", now)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw
	if err = s.IssueAccess(ctx, token, "admin"); err != nil {
		t.Fatal(err)
	}
	_, err = s.RedeemAccess(ctx, token.Hash, AccessInvite, newInput("mail-conflict", "Mail-use"), hash, now)
	if !errors.Is(err, ErrUsernameConflict) {
		t.Fatal(err)
	}
	list, _ := s.ListRegistrationCodes(ctx)
	for _, c := range list {
		if c.Code == "Mail-use" && c.Used != 0 {
			t.Fatal("failed mail registration consumed code")
		}
	}
	if _, err = s.RedeemAccess(ctx, token.Hash, AccessInvite, newInput("mail-new", "Mail-use"), hash, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LookupAccess(ctx, token.Hash, AccessInvite, now); !errors.Is(err, ErrAccessInvalid) {
		t.Fatal("mail token not consumed")
	}
	// Simulate an unavailable destination after validation. Neither state changes.
	oldPath := s.path
	s.path = filepath.Join(path, "unwritable.json")
	if _, err = s.CreateUser(ctx, UserMutation{}, newInput("write-failed", "Fail-save"), hash); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	s.path = oldPath
	if _, err = s.FindUserByLogin(ctx, "write-failed"); !errors.Is(err, ErrUserNotFound) {
		t.Fatal("failed write changed users")
	}
	reopened, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	list, err = reopened.ListRegistrationCodes(ctx)
	if err != nil || len(list) != 3 {
		t.Fatal(err, len(list))
	}
	for _, c := range list {
		want := int64(1)
		if c.Code == "Fail-save" {
			want = 0
		}
		if c.Used != want {
			t.Fatal("persisted use mismatch", c.Code, c.Used)
		}
	}
	// Unrelated identity writes retain the inventory.
	if err = reopened.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.CreateUser(ctx, UserMutation{}, UserInput{Username: "ordinary", RoleSlugs: []string{"subscriber"}}, hash); err != nil {
		t.Fatal(err)
	}
	if _, err = NewFileUserStore(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"registration_codes"`) {
		t.Fatal("inventory lost")
	}
	// A caller cannot mutate the private inventory via returned time pointers.
	for i := range list {
		if list[i].LastUsedAt != nil {
			*list[i].LastUsedAt = time.Time{}
		}
	}
	again, _ := reopened.ListRegistrationCodes(ctx)
	for _, c := range again {
		if !ValidRegistrationCode(c) {
			t.Fatal("mutable inventory projection")
		}
	}
	_, err = reopened.MutateRegistrationCodes(ctx, RegistrationCodeMutation{Action: "update", Targets: []RegistrationCodeTarget{{codes[0].ID, 1}}, Patch: RegistrationCodePatch{Note: ptrCode("stale")}}, "admin", now)
	if !errors.Is(err, ErrRegistrationCodeConflict) {
		t.Fatal("usage did not invalidate admin version", err)
	}
}

func ptrCode(s string) *string { return &s }
