package site

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func TestModuleSettingsPersistenceIsolationAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site-settings.json")
	m, err := NewManager(path, DefaultSettings("Site", "Tagline"))
	if err != nil {
		t.Fatal(err)
	}
	before := m.Snapshot()
	candidate := before.Settings
	candidate.Modules = map[string]bool{"comments": false}
	after, fields, err := m.Save(before.Revision, candidate)
	if err != nil || len(fields) > 0 {
		t.Fatalf("%v %v", fields, err)
	}
	candidate.Modules["comments"] = true
	after.Settings.Modules["comments"] = true
	if m.ModuleEnabled("comments") {
		t.Fatal("saved settings alias caller map")
	}
	if _, _, err = m.Save(before.Revision, candidate); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal("stale save not rejected")
	}
	reopened, err := NewManager(path, DefaultSettings("Other", "Other"))
	if err != nil || reopened.ModuleEnabled("comments") {
		t.Fatal("module state lost on restart", err)
	}
	var export bytes.Buffer
	if err = m.Export(&export); err != nil {
		t.Fatal(err)
	}
	other, _ := NewManager("", DefaultSettings("Other", "Other"))
	if _, fields, err = other.Import(&export, other.Snapshot().Revision); err != nil || len(fields) > 0 || other.ModuleEnabled("comments") {
		t.Fatal("export/import lost module overrides", fields, err)
	}
	bad := m.Snapshot()
	bad.Settings.Modules["plugin-jobs"] = true
	if _, fields, err = m.Save(bad.Revision, bad.Settings); err != nil || len(fields) == 0 {
		t.Fatal("reserved capability was enabled")
	}
}
