package site

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInitialSettingsPersistBeforeFirstAdminSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site-settings.json")
	initial, err := NewManager(path, DefaultSettings("First installation", "Initial tagline"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil || initial.Snapshot().UpdatedAt.IsZero() {
		t.Fatalf("initial settings were not persisted: %v", err)
	}
	unchanged := initial.Snapshot()
	if _, _, err := initial.Save(unchanged.Revision, unchanged.Settings); err != nil {
		t.Fatal(err)
	}
	// Even invalid, obsolete bootstrap values must not prevent a saved site
	// from starting, or change the reset target after a redeployment.
	reopened, err := NewManager(path, DefaultSettings(strings.Repeat("x", 121), "Changed env"))
	if err != nil || !reflect.DeepEqual(reopened.Snapshot().Settings, unchanged.Settings) {
		t.Fatalf("bootstrap replaced persisted settings: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("unchanged save or restart rewrote the persisted document")
	}
	saved := reopened.Snapshot()
	saved.Settings.Identity.Title = "Saved in admin"
	if _, fields, err := reopened.Save(saved.Revision, saved.Settings); err != nil || len(fields) != 0 {
		t.Fatalf("save: %v %v", fields, err)
	}
	reopened, err = NewManager(path, Settings{})
	if err != nil || reopened.Snapshot().Settings.Identity.Title != "Saved in admin" {
		t.Fatalf("saved settings lost on restart: %v", err)
	}
	reset, err := reopened.Reset(reopened.Snapshot().Revision)
	if err != nil || !reflect.DeepEqual(reset.Settings, DefaultSettings("", "")) {
		t.Fatalf("reset depended on environment: %v", err)
	}
}

func TestBootstrapCannotReplaceCorruptSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site-settings.json")
	const corrupt = `{"schema":1,"settings":{}}`
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(path, DefaultSettings("New env", "New env")); err == nil {
		t.Fatal("corrupt saved settings were replaced by bootstrap defaults")
	}
	after, _ := os.ReadFile(path)
	if string(after) != corrupt {
		t.Fatal("corrupt document was overwritten")
	}
}

func TestInvalidBootstrapAndUnwritableDestinationFail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site-settings.json")
	if _, err := NewManager(path, DefaultSettings(strings.Repeat("x", 121), "")); err == nil {
		t.Fatal("invalid initial settings accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid bootstrap created a document")
	}
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(filepath.Join(path, "child.json"), DefaultSettings("", "")); err == nil {
		t.Fatal("initial persistence failure was ignored")
	}
}
