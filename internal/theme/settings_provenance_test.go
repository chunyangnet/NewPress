package theme

import (
	"path/filepath"
	"reflect"
	"testing"

	"newpress.local/newpress/internal/extension"
)

func TestSettingsProvenance(t *testing.T) {
	m, parent := themeOTAFixture(t)
	child := childFixture(t, m, parent)
	if err := m.LoadExplicit(child.Slug); err != nil {
		t.Fatal(err)
	}
	snapshot := m.SettingsSnapshot()
	if snapshot.Parent == nil || snapshot.Parent.Version != parent.Version {
		t.Fatalf("parent: %#v", snapshot.Parent)
	}
	if snapshot.Groups[0].Fields[0].Source != "继承父主题字段" {
		t.Fatal(snapshot.Groups)
	}
	snapshot.Parent.Version = "mutated"
	if m.SettingsSnapshot().Parent.Version != parent.Version {
		t.Fatal("aliased parent")
	}
	child.SettingsSchema = "settings.schema.json"
	if err := extension.WriteJSON(filepath.Join(m.root, child.Slug, "theme.json"), child); err != nil {
		t.Fatal(err)
	}
	childWrite(t, m, child.Slug, child.SettingsSchema, `{"type":"object","properties":{"headline":{"default":""},"child_flag":{"type":"boolean","title":"Child flag","default":false}}}`)
	if err := m.LoadExplicit(child.Slug); err != nil {
		t.Fatal(err)
	}
	snapshot = m.SettingsSnapshot()
	fields := map[string]SettingField{}
	for _, group := range snapshot.Groups {
		for _, field := range group.Fields {
			fields[field.Key] = field
		}
	}
	if fields["headline"].Source != "子主题调整默认值" || fields["headline"].DefaultHint != "有效默认值：空字符串" {
		t.Fatal(fields)
	}
	if fields["child_flag"].Source != "子主题新增字段" || fields["child_flag"].DefaultHint != "有效默认值：关闭" {
		t.Fatal(fields)
	}
	preview, _, _, errors := m.PreviewSettings(snapshot.Values)
	if len(errors) > 0 || !reflect.DeepEqual(snapshot.Groups, preview.Groups) || !reflect.DeepEqual(snapshot.Parent, preview.Parent) {
		t.Fatal("preview provenance", errors)
	}
	childWrite(t, m, child.Slug, child.SettingsSchema, `{"type":"object","properties":{"headline":{"title":"Child title"}}}`)
	if err := m.LoadExplicit(child.Slug); err != nil {
		t.Fatal(err)
	}
	if got := m.SettingsSnapshot().Groups[0].Fields[0].Source; got != "继承父主题字段 · 子主题调整说明" {
		t.Fatal(got)
	}
	if err := m.LoadExplicit(parent.Slug); err != nil {
		t.Fatal(err)
	}
	if got := m.SettingsSnapshot(); got.Parent != nil || got.Groups[0].Fields[0].Source != "" {
		t.Fatal("parent provenance leaked")
	}
}

func TestSettingDefaultHintZero(t *testing.T) {
	for _, value := range []any{float64(0), int64(0)} {
		if got := settingDefaultHint("count", settingProperty{Type: "integer", Default: value}); got != "有效默认值：0" {
			t.Fatal(got)
		}
	}
}
