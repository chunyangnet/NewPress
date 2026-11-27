package plugin

import (
	"path/filepath"
	"reflect"
	"testing"
)

func settingsManifest(t *testing.T) Manifest {
	t.Helper()
	m, err := readManifest(filepath.Join("..", "..", "examples", "plugins", "content-age", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSettingsRegistrationLegacyAndOrderedSections(t *testing.T) {
	m := settingsManifest(t)
	legacy, err := SettingsPageFor(m)
	if err != nil || len(legacy.Groups) != 1 || legacy.Groups[0].ID != "general" || len(legacy.Groups[0].Fields) != 3 {
		t.Fatalf("legacy registration: %#v, %v", legacy, err)
	}
	m.Admin = &AdminContribution{Settings: &SettingsRegistration{Schema: 1, Sections: []SettingsSection{
		{ID: "copy", Title: "提醒文案", Order: 20, Fields: []string{"title"}},
		{ID: "rules", Title: "提醒条件", Order: 10, Fields: []string{"days"}},
	}}}
	page, err := SettingsPageFor(m)
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != "/admin/plugins/content-age/" || page.Adapter != "configuration" || !reflect.DeepEqual([]string{page.Groups[0].ID, page.Groups[1].ID, page.Groups[2].ID}, []string{"rules", "copy", "general"}) || page.Groups[2].Fields[0].Key != "message" {
		t.Fatalf("registered page: %#v", page)
	}
	m.Admin.Settings.Sections[0].ID = "general"
	page, err = SettingsPageFor(m)
	if err != nil || len(page.Groups) != 2 || len(page.Groups[1].Fields) != 2 {
		t.Fatalf("general fallback: %#v, %v", page, err)
	}
}

func TestSettingsRegistrationRejectsInvalidDeclarations(t *testing.T) {
	for name, registration := range map[string]SettingsRegistration{
		"schema":               {Schema: 2},
		"unknown":              {Schema: 1, Sections: []SettingsSection{{ID: "main", Title: "常规", Fields: []string{"unknown"}}}},
		"duplicate":            {Schema: 1, Sections: []SettingsSection{{ID: "a", Title: "甲", Fields: []string{"title"}}, {ID: "b", Title: "乙", Fields: []string{"title"}}}},
		"duplicate section":    {Schema: 1, Sections: []SettingsSection{{ID: "a", Title: "甲", Fields: []string{"title"}}, {ID: "a", Title: "乙", Fields: []string{"days"}}}},
		"permissions reserved": {Schema: 1, Sections: []SettingsSection{{ID: "permissions", Title: "权限", Fields: []string{"title"}}}},
		"records reserved":     {Schema: 1, Sections: []SettingsSection{{ID: "records", Title: "记录", Fields: []string{"title"}}}},
		"unsafe id":            {Schema: 1, Sections: []SettingsSection{{ID: "../escape", Title: "常规", Fields: []string{"title"}}}},
		"empty":                {Schema: 1, Sections: []SettingsSection{{ID: "main", Title: "常规"}}},
	} {
		t.Run(name, func(t *testing.T) {
			m := settingsManifest(t)
			m.Admin = &AdminContribution{Settings: &registration}
			if ValidateManifest(m) == nil {
				t.Fatal("invalid settings registration accepted")
			}
		})
	}
}

func TestSettingsRegistryIsolationAndLifecycle(t *testing.T) {
	m, manifest, _ := businessFixture(t)
	r := NewSettingsRegistry()
	if err := r.Register(manifest); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(manifest); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	pages := r.Pages()
	pages[0].Groups[0].Fields[0].Label = "changed"
	if r.Pages()[0].Groups[0].Fields[0].Label == "changed" {
		t.Fatal("caller mutated registry")
	}
	if err := m.SetEnabled(manifest.Slug, false); err != nil {
		t.Fatal(err)
	}
	pages = m.SettingsPages()
	if len(pages) != 1 || pages[0].Slug != manifest.Slug {
		t.Fatalf("disabled plugin lost settings: %#v", pages)
	}
	if err := m.Uninstall(manifest.Slug); err != nil {
		t.Fatal(err)
	}
	if len(m.SettingsPages()) != 0 {
		t.Fatal("removed plugin left stale settings")
	}
}
