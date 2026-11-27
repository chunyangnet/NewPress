package theme

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/extension"
)

func childFixture(t *testing.T, m *Manager, parent Manifest) Manifest {
	t.Helper()
	child := Manifest{Schema: 2, Slug: "child-theme", Name: "Child", Version: "1.0.0", Engine: parent.Engine, View: parent.View, Parent: &ParentTheme{Slug: parent.Slug, Version: ">=1.0.0 <2.0.0"}}
	d := filepath.Join(m.root, child.Slug)
	if err := os.MkdirAll(d, 0700); err != nil {
		t.Fatal(err)
	}
	if err := extension.WriteJSON(filepath.Join(d, "theme.json"), child); err != nil {
		t.Fatal(err)
	}
	return child
}
func childWrite(t *testing.T, m *Manager, slug, path, value string) {
	t.Helper()
	name := filepath.Join(m.root, slug, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestChildThemeInheritsAndOverrides(t *testing.T) {
	m, parent := themeOTAFixture(t)
	child := childFixture(t, m, parent)
	childWrite(t, m, child.Slug, "templates/single.html", `{{define "content"}}child single{{end}}`)
	childWrite(t, m, child.Slug, "assets/extra.css", "child")
	if err := m.LoadExplicit(child.Slug); err != nil {
		t.Fatal(err)
	}
	for page, want := range map[string]string{"home": "old", "single": "child single"} {
		var b bytes.Buffer
		if err := m.Render(&b, page, nil); err != nil || b.String() != want {
			t.Fatalf("%s: %q %v", page, b.String(), err)
		}
	}
	if m.SettingsSnapshot().Values["headline"] != "default" {
		t.Fatal("child shared parent's saved values")
	}
	for _, path := range []string{"style.css", "extra.css"} {
		if _, _, err := m.ReadVersionedAsset(child.Slug, m.assetRevision, path); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := m.Active()
	snapshot.Parent.Slug = "mutated"
	if m.Active().Parent.Slug != parent.Slug {
		t.Fatal("manifest alias")
	}
	state, _ := m.VersionState(parent.Slug)
	if err := m.Uninstall(t.Context(), parent.Slug, parent.Version, state.Revision, child.Slug, nil); err == nil {
		t.Fatal("uninstalled required parent")
	}
}
func TestChildThemeParentUpdateAndRollback(t *testing.T) {
	m, parent := themeOTAFixture(t)
	child := childFixture(t, m, parent)
	if err := m.LoadExplicit(child.Slug); err != nil {
		t.Fatal(err)
	}
	oldAsset, oldSettings := m.assetRevision, m.SettingsSnapshot().Revision
	target, err := m.CaptureUpdate(parent.Slug, "", "")
	if err != nil {
		t.Fatal(err)
	}
	parent.Version = "1.1.0"
	release, archive := themeOTAArchive(t, parent, otaSettings, `{{define "content"}}new parent{{end}}`)
	if err = m.UpdatePackage(t.Context(), archive, release, target, nil); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err = m.Render(&b, "home", nil); err != nil || b.String() != "new parent" {
		t.Fatal(b.String(), err)
	}
	if m.Active().Slug != child.Slug || oldAsset == m.assetRevision || oldSettings == m.SettingsSnapshot().Revision {
		t.Fatal("child snapshot did not refresh")
	}
	if m.SettingsSnapshot().Parent.Version != "1.1.0" {
		t.Fatal("parent identity not updated")
	}
	if _, _, err = m.ReadVersionedAsset(child.Slug, oldAsset, "style.css"); err == nil {
		t.Fatal("old asset served new data")
	}
	state, _ := m.VersionState(parent.Slug)
	if err = m.Rollback(t.Context(), parent.Slug, parent.Version, state.Revision, "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if m.SettingsSnapshot().Parent.Version != "1.0.0" {
		t.Fatal("parent identity not rolled back")
	}
	b.Reset()
	if err = m.Render(&b, "home", nil); err != nil || b.String() != "old" {
		t.Fatal(b.String(), err)
	}
}
func TestInactiveChildBlocksIncompatibleParentUpdate(t *testing.T) {
	m, parent := themeOTAFixture(t)
	childFixture(t, m, parent)
	target, _ := m.CaptureUpdate(parent.Slug, "", "")
	parent.Version = "2.0.0"
	release, archive := themeOTAArchive(t, parent, otaSettings, `{{define "content"}}bad{{end}}`)
	if err := m.UpdatePackage(t.Context(), archive, release, target, nil); err == nil {
		t.Fatal("broke inactive child")
	}
	if m.Active().Version != "1.0.0" {
		t.Fatal("failed update changed state")
	}
}
func TestChildSettingsOverlay(t *testing.T) {
	m, parent := themeOTAFixture(t)
	child := childFixture(t, m, parent)
	child.SettingsSchema = "settings.schema.json"
	if err := extension.WriteJSON(filepath.Join(m.root, child.Slug, "theme.json"), child); err != nil {
		t.Fatal(err)
	}
	childWrite(t, m, child.Slug, child.SettingsSchema, `{"type":"object","properties":{"headline":{"default":"child default"},"extra":{"type":"boolean","default":true}}}`)
	if err := m.LoadExplicit(child.Slug); err != nil {
		t.Fatal(err)
	}
	if m.SettingsSnapshot().Values["headline"] != "child default" || m.SettingsSnapshot().Values["extra"] != true {
		t.Fatal(m.SettingsSnapshot())
	}
	childWrite(t, m, child.Slug, child.SettingsSchema, `{"type":"object","properties":{"headline":{"type":"integer"}}}`)
	if err := m.LoadExplicit(child.Slug); err == nil {
		t.Fatal("changed inherited type")
	}
}
func TestChildInvalidDependencies(t *testing.T) {
	for _, kind := range []string{"missing", "self", "nested", "view", "version", "schema"} {
		t.Run(kind, func(t *testing.T) {
			m, parent := themeOTAFixture(t)
			child := childFixture(t, m, parent)
			switch kind {
			case "missing":
				child.Parent.Slug = "missing-parent"
			case "self":
				child.Parent.Slug = child.Slug
			case "nested":
				parent.Schema = 2
				parent.Parent = &ParentTheme{Slug: child.Slug, Version: "*"}
				if err := extension.WriteJSON(filepath.Join(m.root, parent.Slug, "theme.json"), parent); err != nil {
					t.Fatal(err)
				}
			case "view":
				child.View = ChunnelViewV1
			case "version":
				child.Parent.Version = ">=2.0.0"
			case "schema":
				child.Schema = 1
			}
			if err := extension.WriteJSON(filepath.Join(m.root, child.Slug, "theme.json"), child); err != nil {
				t.Fatal(err)
			}
			if err := m.LoadExplicit(child.Slug); err == nil {
				t.Fatal("invalid dependency accepted")
			}
		})
	}
}
func TestChildAssetShadowingAndAliases(t *testing.T) {
	m, parent := themeOTAFixture(t)
	child := childFixture(t, m, parent)
	childWrite(t, m, child.Slug, "assets/style.css", "override")
	if err := m.LoadExplicit(child.Slug); err != nil {
		t.Fatal(err)
	}
	revision := m.assetRevision
	childWrite(t, m, parent.Slug, "assets/style.css", "hidden change")
	if err := m.LoadExplicit(child.Slug); err != nil {
		t.Fatal(err)
	}
	if m.assetRevision != revision {
		t.Fatal("shadowed asset changed identity")
	}
	if err := os.Remove(filepath.Join(m.root, child.Slug, "assets", "style.css")); err != nil {
		t.Fatal(err)
	}
	childWrite(t, m, child.Slug, "assets/Style.css", "alias")
	if err := m.LoadExplicit(child.Slug); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatal("alias accepted", err)
	}
}
