package auth

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRegistrationCodeGenerationRetriesExistingAndBatchCollisions(t *testing.T) {
	// Each byte supplies one digit to crypto/rand.Int. Force collisions with
	// inventory and within the new batch before providing enough fresh codes.
	var random []byte
	for _, candidate := range []string{"000000", "000000", "123456", "123456", "000001", "654321", "987654"} {
		for _, digit := range []byte(candidate) {
			random = append(random, digit-'0')
		}
	}
	excluded := map[string]bool{"VIP-000-000": true, "VIP-000-001": true}
	got, err := generateRegistrationCodes(RegistrationCodeGenerator{Count: 3, Prefix: "VIP-", Length: 6, Charset: "digits", Group: 3}, excluded, bytes.NewReader(random))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"VIP-123-456", "VIP-654-321", "VIP-987-654"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("generation did not replenish collided codes: got %v, want %v", got, want)
	}
	if len(excluded) != 2 {
		t.Fatal("generation changed the inventory exclusion set")
	}
}

func TestFileRegistrationCodeGenerationAtInventoryLimit(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "users.json")
	store, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	seen := make(map[string]bool, MaxRegistrationCodes)
	for offset := 0; offset < 4500; offset += MaxRegistrationCodeBatch {
		codes := make([]string, MaxRegistrationCodeBatch)
		for i := range codes {
			codes[i] = fmt.Sprintf("%06d", offset+i)
			seen[codes[i]] = true
		}
		if _, err := store.MutateRegistrationCodes(ctx, RegistrationCodeMutation{Action: "create", Codes: codes, Rules: RegistrationCodeRules{Enabled: true, MaxUses: 1}}, "test", now); err != nil {
			t.Fatal(err)
		}
	}
	// All batches use the same shortest numeric format and race against the
	// same inventory. Every request must receive the entire requested batch.
	type result struct {
		items []RegistrationCode
		err   error
	}
	results := make(chan result, 10)
	start := make(chan struct{})
	for range 10 {
		go func() {
			<-start
			items, err := store.MutateRegistrationCodes(ctx, RegistrationCodeMutation{Action: "generate", Generator: RegistrationCodeGenerator{Count: 50, Length: 6, Charset: "digits"}, Rules: RegistrationCodeRules{Enabled: true, MaxUses: 3, Note: "generated"}}, "test", now)
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
	if len(seen) != MaxRegistrationCodes {
		t.Fatal("unexpected inventory size", len(seen))
	}
	_, err = store.MutateRegistrationCodes(ctx, RegistrationCodeMutation{Action: "generate", Generator: RegistrationCodeGenerator{Count: 1, Length: 6, Charset: "digits"}}, "test", now)
	if !errors.Is(err, ErrRegistrationCodeLimit) {
		t.Fatal("generation ignored the inventory limit", err)
	}
	reopened, err := NewFileUserStore(path)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := reopened.ListRegistrationCodes(ctx)
	if err != nil || len(stored) != MaxRegistrationCodes {
		t.Fatal("generated inventory was not persisted atomically", len(stored), err)
	}
	for _, code := range stored {
		if !seen[code.Code] {
			t.Fatal("unexpected persisted registration code")
		}
	}
}

func TestFileRegistrationCodeReimportRejectsDeletedRecordTargets(t *testing.T) {
	ctx := t.Context()
	store, err := NewFileUserStore(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureRoles(ctx, BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	create := RegistrationCodeMutation{Action: "create", Codes: []string{"Reimport-001"}, Rules: RegistrationCodeRules{Enabled: true, MaxUses: 1}}
	original, err := store.MutateRegistrationCodes(ctx, create, "test", now)
	if err != nil {
		t.Fatal(err)
	}
	stale := []RegistrationCodeTarget{{ID: original[0].ID, Version: original[0].Version}}
	if _, err := store.MutateRegistrationCodes(ctx, RegistrationCodeMutation{Action: "delete", Targets: stale}, "test", now); err != nil {
		t.Fatal(err)
	}
	reimported, err := store.MutateRegistrationCodes(ctx, create, "test", now)
	if err != nil {
		t.Fatal(err)
	}
	if reimported[0].ID == original[0].ID || reimported[0].Version != 1 {
		t.Fatal("reimport reused the deleted registration record's identity")
	}
	note := "stale editor"
	for _, action := range []string{"update", "delete"} {
		_, err := store.MutateRegistrationCodes(ctx, RegistrationCodeMutation{Action: action, Targets: stale, Patch: RegistrationCodePatch{Note: &note}}, "test", now)
		if !errors.Is(err, ErrRegistrationCodeConflict) {
			t.Fatal("deleted record target affected the replacement", action, err)
		}
	}
	reopened, err := NewFileUserStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := reopened.ListRegistrationCodes(ctx)
	if err != nil || len(stored) != 1 || stored[0].ID != reimported[0].ID || stored[0].Note != "" || stored[0].Version != 1 {
		t.Fatal("stale mutation changed the persisted replacement", stored, err)
	}
}
