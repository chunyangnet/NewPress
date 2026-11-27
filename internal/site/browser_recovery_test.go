package site

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestBrowserRecoveryOptInPersistsAndExports(t *testing.T) {
	defaults := DefaultSettings("Recovery", "Browser copies")
	encoded, err := json.Marshal(defaults)
	if err != nil || defaults.Writing.BrowserRecoveryEnabled || !bytes.Contains(encoded, []byte(`"browser_recovery_enabled":false`)) {
		t.Fatalf("browser recovery must be explicitly disabled by default: %s, %v", encoded, err)
	}
	path := filepath.Join(t.TempDir(), "site.json")
	manager, err := NewManager(path, defaults)
	if err != nil {
		t.Fatal(err)
	}
	initial := manager.Snapshot()
	p := defaults.RegistrationPolicy()
	defaults.Accounts = &AccountSettings{Registration: "closed", Policy: &p}
	values := formValues(initial.Settings)
	values.Set("browser_recovery_enabled", "true")
	settings, fields, err := ParseForm(values)
	if err != nil || len(fields) > 0 || !settings.Writing.BrowserRecoveryEnabled {
		t.Fatalf("parse: %v %v", fields, err)
	}
	saved, fields, err := manager.Save(initial.Revision, settings)
	if err != nil || len(fields) > 0 || saved.Revision == initial.Revision {
		t.Fatalf("save: %v %v", fields, err)
	}
	reloaded, err := NewManager(path, defaults)
	if err != nil || !reloaded.Snapshot().Settings.Writing.BrowserRecoveryEnabled {
		t.Fatalf("reopen: %v", err)
	}
	var export bytes.Buffer
	if err := reloaded.Export(&export); err != nil {
		t.Fatal(err)
	}
	target, err := NewManager("", defaults)
	if err != nil {
		t.Fatal(err)
	}
	imported, fields, err := target.Import(&export, target.Snapshot().Revision)
	if err != nil || len(fields) > 0 || !imported.Settings.Writing.BrowserRecoveryEnabled {
		t.Fatalf("import: %v %v", fields, err)
	}
	values.Del("browser_recovery_enabled")
	disabled, fields, err := ParseForm(values)
	if err != nil || len(fields) > 0 || disabled.Writing.BrowserRecoveryEnabled {
		t.Fatalf("disable: %v %v", fields, err)
	}
	if settingsRevision(disabled) != settingsRevision(defaults) {
		t.Fatal("disabling opt-in changed the original settings representation")
	}
}
