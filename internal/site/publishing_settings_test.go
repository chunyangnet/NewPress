package site

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestPublishingSettingsPersistAndRemainOptional(t *testing.T) {
	defaults := DefaultSettings("NewPress", "")
	payload, err := json.Marshal(defaults)
	if err != nil {
		t.Fatal(err)
	}
	var old Settings
	if err = json.Unmarshal(payload, &old); err != nil {
		t.Fatal(err)
	}
	normalized, fields := Validate(old)
	if len(fields) != 0 || settingsRevision(normalized) != settingsRevision(defaults) {
		t.Fatal("legacy settings changed", fields)
	}
	path := filepath.Join(t.TempDir(), "site.json")
	m, err := NewManager(path, defaults)
	if err != nil {
		t.Fatal(err)
	}
	snap := m.Snapshot()
	snap.Settings.Writing.PostPermalink = "date"
	snap.Settings.Writing.PagePermalink = "id"
	snap.Settings.Writing.MaxUploadMiB = 128
	snap.Settings.Writing.SlugMode = "manual"
	saved, fields, err := m.Save(snap.Revision, snap.Settings)
	if err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	loaded, err := NewManager(path, defaults)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Snapshot().Settings.Writing != saved.Settings.Writing {
		t.Fatal("publishing settings not persisted")
	}
	for _, limit := range []int{-1, 1025} {
		v := defaults
		v.Writing.MaxUploadMiB = limit
		if _, f := Validate(v); f["max_upload_mib"] == "" {
			t.Fatal("invalid upload limit accepted")
		}
	}
	bad := defaults
	bad.Writing.PostPermalink = "/admin/{slug}"
	bad.Writing.PagePermalink = "date"
	bad.Writing.SlugMode = "unknown"
	if _, f := Validate(bad); len(f) != 3 {
		t.Fatal("invalid publishing choices", f)
	}
}
