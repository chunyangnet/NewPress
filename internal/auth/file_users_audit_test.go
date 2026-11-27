package auth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/audit"
)

func TestFileUserStoreWritesRedactedAuditEvents(t *testing.T) {
	directory := t.TempDir()
	ledgerPath := filepath.Join(directory, "audit.jsonl")
	ledger, err := audit.Open(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewFileUserStore(filepath.Join(directory, "users.json"), ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureRoles(context.Background(), BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	passwordHash, err := HashPassword("File audit password 2026")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(context.Background(), UserMutation{Actor: "bootstrap-owner"}, UserInput{
		Username: "audit-user", Email: "private-user@example.test", DisplayName: "审计用户",
		Status: UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"administrator"},
	}, passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	user, err = store.UpdateUser(context.Background(), user.ID, UserMutation{ExpectedVersion: user.Version, Actor: "account-owner"}, UserInput{
		Username: user.Username, Email: user.Email, DisplayName: "Updated display", Status: user.Status, Locale: user.Locale, RoleSlugs: []string{"administrator"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rotatedHash, err := HashPassword("Rotated file audit password 2026")
	if err != nil {
		t.Fatal(err)
	}
	user, err = store.UpdateUserPassword(context.Background(), user.ID, UserMutation{ExpectedVersion: user.Version, Actor: "account-owner"}, rotatedHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordUserLogin(context.Background(), user.ID, user.CreatedAt); err != nil {
		t.Fatal(err)
	}
	records := ledger.Records()
	actions := make(map[string]bool)
	for _, record := range records {
		if record.Phase == "commit" {
			actions[record.Action] = true
		}
	}
	for _, action := range []string{"user.roles.ensure", "user.created", "user.updated", "user.password_changed", "user.login_recorded"} {
		if !actions[action] {
			t.Fatalf("missing committed action %s in %#v", action, actions)
		}
	}
	payload, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-user@example.test", passwordHash, rotatedHash} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("audit ledger leaked sensitive user data %q", secret)
		}
	}
}
