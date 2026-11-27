package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/dirlock"
)

func adminFixture(t *testing.T) (*auth.FileUserStore, auth.User, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := auth.NewFileUserStore(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureRoles(t.Context(), auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("original administrator password")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(t.Context(), auth.UserMutation{Actor: "fixture"}, auth.UserInput{
		Username: "owner", Email: "owner@example.test", RoleSlugs: []string{"administrator"},
	}, hash)
	if err != nil {
		t.Fatal(err)
	}
	return store, user, dir
}

func TestAdminPasswordInputRejectsOversizeMultilineAndControls(t *testing.T) {
	for _, input := range []string{"short", "password with\nextra lines", "password with\x00controls", strings.Repeat("x", 1025), strings.Repeat("x", 2048) + "\n", string([]byte{0xff})} {
		if _, err := readAdminPassword(strings.NewReader(input)); err == nil {
			t.Fatal("accepted invalid input")
		}
	}
	input := "  a long password with spaces  "
	if value, err := readAdminPassword(strings.NewReader(input + "\r\n")); err != nil || value != input {
		t.Fatalf("password whitespace was not preserved: %v", err)
	}
}

func TestAdminPasswordPipeRejectsCharacterDeviceAndAcceptsPrivateFile(t *testing.T) {
	original := os.Stdin
	t.Cleanup(func() { os.Stdin = original })
	device, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	os.Stdin = device
	if _, err = readAdminPasswordPipe(); err == nil {
		t.Fatal("character device accepted as hidden password input")
	}
	path := filepath.Join(t.TempDir(), "password")
	if err = os.WriteFile(path, []byte("private password from pipe\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	os.Stdin = file
	if value, err := readAdminPasswordPipe(); err != nil || value != "private password from pipe" {
		t.Fatal("redirected password was rejected", err)
	}
}

func TestAdminListProjectsOnlySafeFields(t *testing.T) {
	store, _, _ := adminFixture(t)
	items, err := listAdministrators(t.Context(), store)
	if err != nil || len(items) != 1 || items[0].Username != "owner" {
		t.Fatal("administrator projection failed", err)
	}
	data, _ := json.Marshal(items)
	for _, secret := range []string{"password", "argon2", "email", "original administrator password", "owner@example.test"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("account list exposed a private field")
		}
	}
}

func TestAdminPasswordResetRevokesSessionAndRejectsStaleVersion(t *testing.T) {
	store, user, _ := adminFixture(t)
	manager, err := auth.New(auth.Config{Users: store})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := manager.AuthenticateContext(t.Context(), "before", "owner", "original administrator password")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := changeAdministrator(t.Context(), store, "reset-password", user.ID, user.Version, "", "replacement administrator password")
	if err != nil || changed.Version != user.Version+1 {
		t.Fatal("password reset failed", err)
	}
	if _, valid := manager.ValidateSessionContext(t.Context(), token); valid {
		t.Fatal("old session remains valid")
	}
	if _, _, err = manager.AuthenticateContext(t.Context(), "old", "owner", "original administrator password"); err == nil {
		t.Fatal("old password remains valid")
	}
	if _, _, err = manager.AuthenticateContext(t.Context(), "new", "owner", "replacement administrator password"); err != nil {
		t.Fatal("new password rejected", err)
	}
	if _, err = changeAdministrator(t.Context(), store, "rename", user.ID, user.Version, "renamed", ""); err == nil {
		t.Fatal("stale version accepted")
	}
}

func TestAdminCannotModifyNonAdministratorOrSuspendedAccount(t *testing.T) {
	store, user, _ := adminFixture(t)
	for _, input := range []auth.UserInput{
		{Username: "writer", RoleSlugs: []string{"author"}},
		{Username: "disabled", RoleSlugs: []string{"administrator"}, Status: auth.UserStatusSuspended},
	} {
		other, err := store.CreateUser(t.Context(), auth.UserMutation{}, input, user.PasswordHash)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = changeAdministrator(t.Context(), store, "reset-password", other.ID, other.Version, "", "new administrator password"); err == nil {
			t.Fatal("modified an ineligible account")
		}
	}
	if _, err := changeAdministrator(t.Context(), store, "rename", user.ID, user.Version, "writer", ""); err == nil {
		t.Fatal("duplicate login name accepted")
	}
}

func TestAdminFileMutationUsesLockAndAuditAndPreservesIdentity(t *testing.T) {
	_, user, dir := adminFixture(t)
	cfg := config.Config{DataDir: dir}
	locks, err := dirlock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = withAdminStore(t.Context(), cfg, true, func(auth.UserStore) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("mutation ignored a running application's lock")
	}
	// Read-only queries may read the atomically replaced user file while serving.
	err = withAdminStore(t.Context(), cfg, false, func(store auth.UserStore) error {
		_, err := listAdministrators(t.Context(), store)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	locks.Close()
	err = withAdminStore(t.Context(), cfg, true, func(store auth.UserStore) error {
		updated, err := changeAdministrator(t.Context(), store, "rename", user.ID, user.Version, "new-owner", "")
		if err == nil && (updated.ID != user.ID || updated.Email != user.Email || updated.PasswordHash != user.PasswordHash || !isAdministrator(updated)) {
			t.Fatal("rename changed unrelated identity fields")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ledgerBytes, err := os.ReadFile(filepath.Join(dir, audit.FileName))
	if err != nil || !strings.Contains(string(ledgerBytes), "local-admin-cli") || !strings.Contains(string(ledgerBytes), "user.updated") || strings.Contains(string(ledgerBytes), user.PasswordHash) {
		t.Fatal("audit event missing or contains credentials", err)
	}
	err = withAdminStore(context.Background(), cfg, true, func(store auth.UserStore) error {
		_, err := changeAdministrator(t.Context(), store, "reset-password", user.ID, user.Version+1, "", "replacement administrator password")
		return err
	})
	if err != nil {
		t.Fatal("audit chain could not be continued", err)
	}
	ledger, err := audit.Open(filepath.Join(dir, audit.FileName))
	if err != nil || ledger.ResolvePaths(map[string]string{audit.FactUsers: filepath.Join(dir, "users.json")}) != nil {
		t.Fatal("account audit chain did not verify", err)
	}
}

func TestAdminRejectsUnknownAndIrrelevantArgumentsBeforeStorage(t *testing.T) {
	for _, arguments := range [][]string{{"list", "--username", "owner"}, {"reset-password", "--id", "1"}, {"rename", "--id", "1", "--expected-version", "1", "--username", "owner", "--password-stdin"}, {"reset-password", "--password", "do-not-echo-this-secret"}} {
		err := adminCommand(arguments)
		if err == nil || strings.Contains(err.Error(), "do-not-echo-this-secret") {
			t.Fatal("invalid arguments accepted or secret echoed")
		}
	}
}
