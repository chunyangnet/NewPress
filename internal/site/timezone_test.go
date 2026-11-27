package site

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExplicitTimezoneRequired(t *testing.T) {
	for _, zone := range []string{"", "  ", "Local", "Mars/Olympus", "UTC", "Asia/Shanghai", "America/New_York", "Asia/Kathmandu"} {
		t.Run(zone, func(t *testing.T) {
			settings := DefaultSettings("Test", "Test")
			settings.Identity.Timezone = zone
			_, fields := Validate(settings)
			valid := zone == "UTC" || zone == "Asia/Shanghai" || zone == "America/New_York" || zone == "Asia/Kathmandu"
			if (fields["site_timezone"] == "") != valid {
				t.Fatalf("zone %q: %v", zone, fields)
			}
			if _, err := NewManager("", settings); (err == nil) != valid {
				t.Fatalf("bootstrap %q: %v", zone, err)
			}
		})
	}
}

func TestStoredSettingsRequireExplicitTimezone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site.json")
	settings := DefaultSettings("Timezone", "")
	settings.Identity.Timezone = ""
	doc := document{Schema: documentSchema, Kind: documentKind, Revision: settingsRevision(settings), Settings: settings, UpdatedAt: time.Now().UTC()}
	if err := writeDocument(path, doc); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := NewManager(path, DefaultSettings("Bootstrap", "")); err == nil {
		t.Fatal("empty timezone loaded")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected document was modified")
	}
	target, _ := NewManager("", DefaultSettings("Import", ""))
	if _, _, err := target.Import(bytes.NewReader(before), target.Snapshot().Revision); err == nil {
		t.Fatal("empty timezone imported")
	}
}

func TestLegacyLocalIsRejectedAndResolverNeverUsesHostZone(t *testing.T) {
	settings := DefaultSettings("Legacy", "Legacy")
	settings.Identity.Timezone = "Local"
	if TimeLocation(settings) != time.UTC {
		t.Fatal("resolver used host zone")
	}
	path := filepath.Join(t.TempDir(), "site.json")
	if err := writeDocument(path, document{Schema: documentSchema, Kind: documentKind, Revision: settingsRevision(settings), Settings: settings}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(path, DefaultSettings("", "")); err == nil {
		t.Fatal("legacy Local accepted")
	}
}
