package plugin

import (
	"reflect"
	"testing"
)

func TestSettingsSnapshotPreservesValidation(t *testing.T) {
	for _, kind := range []string{"valid", "disabled", "error", "unofficial", "invalid-slug", "invalid-ui", "runtime", "no-hooks", "no-config", "capabilities-only"} {
		t.Run(kind, func(t *testing.T) {
			m := settingsManifest(t)
			item := Installed{Slug: m.Slug, Manifest: m, Enabled: true, Official: true}
			switch kind {
			case "disabled":
				item.Enabled = false
			case "error":
				item.Error = "damaged package"
			case "unofficial":
				item.Official = false
			case "invalid-slug":
				item.Manifest.Slug = "../outside"
			case "invalid-ui":
				item.Manifest.Admin = &AdminContribution{Settings: &SettingsRegistration{Schema: 2}}
			case "runtime":
				item.Manifest.Runtime = "unsupported"
			case "no-hooks":
				item.Manifest.Hooks = nil
			case "no-config":
				item.Manifest.Settings, item.Manifest.Capabilities = nil, nil
			case "capabilities-only":
				item.Manifest.Settings = nil
			}
			pages := SettingsPagesFromInstalled([]Installed{item})
			want := kind == "valid" || kind == "disabled" || kind == "capabilities-only"
			if (len(pages) == 1) != want || len(pages) > 1 {
				t.Fatalf("settings filter changed: %#v", pages)
			}
		})
	}
}

func TestSettingsSnapshotIsolationAndLifecycle(t *testing.T) {
	m, manifest, _ := businessFixture(t)
	if err := m.InstallBundled(AIProMax); err != nil {
		t.Fatal(err)
	}
	items, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	pages := SettingsPagesFromInstalled(items)
	if !reflect.DeepEqual(pages, m.SettingsPages()) || len(pages) != 2 || pages[0].Slug != AIProMax || pages[1].Slug != manifest.Slug {
		t.Fatalf("snapshot differs from live projection: %#v", pages)
	}
	if err := m.Uninstall(manifest.Slug); err != nil {
		t.Fatal(err)
	}
	pages[1].Groups[0].Fields[0].Label = "caller changed field"
	pages[0].Groups[0].Title = "caller changed group"
	pages[0].URL = "/caller"
	projected := SettingsPagesFromInstalled(append(items, items...))
	if len(projected) != 2 || projected[1].Groups[0].Fields[0].Label == "caller changed field" || projected[0].Groups[0].Title == "caller changed group" || projected[0].URL == "/caller" {
		t.Fatal("caller changed snapshot or duplicate registrations were accepted")
	}
	// The supplied snapshot is stable; the next live read observes removal.
	next := m.SettingsPages()
	if len(next) != 1 || next[0].Slug != AIProMax {
		t.Fatalf("removed plugin retained across requests: %#v", next)
	}
}
