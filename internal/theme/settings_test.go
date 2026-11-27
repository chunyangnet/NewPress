package theme

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChunnelSavedPaletteIsPreserved(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, version, accent, strong, wantAccent, wantStrong string
	}{
		{"legacy defaults", "0.22.5", "#17C3B2", "#079B91", "#2563eb", "#1d4ed8"},
		{"older defaults", "0.12.0", "#17c3b2", "#079b91", "#2563eb", "#1d4ed8"},
		{"custom accent", "0.22.5", "#663399", "#079b91", "#663399", "#079b91"},
		{"custom strong", "0.22.5", "#17c3b2", "#123456", "#17c3b2", "#123456"},
		{"current choices", "0.22.6", "#17c3b2", "#079b91", "#17c3b2", "#079b91"},
		{"previous launch defaults", "0.44.9", "#2563eb", "#1d4ed8", "#2563eb", "#1d4ed8"},
		{"custom coral before launch", "0.44.9", "#cc785c", "#a9583e", "#cc785c", "#a9583e"},
		{"coral launch defaults", "0.45.0", "#cc785c", "#a9583e", "#2563eb", "#1d4ed8"},
		{"custom coral", "0.45.0", "#cc785c", "#123456", "#cc785c", "#123456"},
		{"current design choices", "0.45.0", "#2563eb", "#1d4ed8", "#2563eb", "#1d4ed8"},
		{"saved after blue default", "0.45.1", "#cc785c", "#a9583e", "#cc785c", "#a9583e"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			document := settingsDocument{Schema: settingsDocumentSchema, Theme: "chunnel", ThemeVersion: test.version,
				Values: map[string]any{"accent": test.accent, "accent_strong": test.strong, "hero_title": "保留站点内容"}}
			if err := writeSettingsDocument(directory, document); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(filepath.Join("..", "..", "themes"), "chunnel", WithSettingsDir(directory))
			if err := manager.Load(); err != nil {
				t.Fatal(err)
			}
			check := func(snapshot SettingsSnapshot) {
				t.Helper()
				if snapshot.Values["accent"] != test.accent || snapshot.Values["accent_strong"] != test.strong || snapshot.Values["hero_title"] != "保留站点内容" {
					t.Fatalf("unexpected migrated values: %#v", snapshot.Values)
				}
			}
			check(manager.SettingsSnapshot())
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, fields, err := manager.ImportSettings(bytes.NewReader(encoded))
			if err != nil || len(fields) != 0 {
				t.Fatalf("import: %v %v", fields, err)
			}
			check(snapshot)
			if err := manager.Load(); err != nil {
				t.Fatal(err)
			}
			check(manager.SettingsSnapshot())
		})
	}
}

func TestChunnelSavedFocusColorIsPreserved(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, accent, focus, wantFocus string
	}{
		{"default focus", "#cc785c", "#CC785C", "#2563eb"},
		{"custom focus", "#cc785c", "#0f766e", "#0f766e"},
		{"custom palette", "#123456", "#cc785c", "#cc785c"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			document := settingsDocument{Schema: settingsDocumentSchema, Theme: "chunnel", ThemeVersion: "0.45.0",
				Values: map[string]any{"accent": test.accent, "accent_strong": "#a9583e", "focus_color": test.focus}}
			if err := writeSettingsDocument(directory, document); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(filepath.Join("..", "..", "themes"), "chunnel", WithSettingsDir(directory))
			if err := manager.Load(); err != nil {
				t.Fatal(err)
			}
			if got := manager.SettingsSnapshot().Values["focus_color"]; got != test.focus {
				t.Fatalf("loaded focus = %v, want %s", got, test.wantFocus)
			}
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, fields, err := manager.ImportSettings(bytes.NewReader(encoded))
			if err != nil || len(fields) != 0 {
				t.Fatalf("import: %v %v", fields, err)
			}
			if got := snapshot.Values["focus_color"]; got != test.focus {
				t.Fatalf("imported focus = %v, want %s", got, test.wantFocus)
			}
		})
	}
}

func TestRetiredChunnelSettingsAreRejected(t *testing.T) {
	manager := NewManager(filepath.Join("..", "..", "themes"), "chunnel", WithSettingsDir(t.TempDir()))
	if err := manager.Load(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"download_module", "hero_enabled", "home_custom_html"} {
		if _, exists := manager.SettingsSnapshot().Values[key]; exists {
			t.Fatal("retired setting remains", key)
		}
		encoded, _ := json.Marshal(settingsDocument{Schema: 1, Theme: "chunnel", Values: map[string]any{key: true}})
		if _, fields, err := manager.ImportSettings(bytes.NewReader(encoded)); err == nil && len(fields) == 0 {
			t.Fatal("retired setting imported", key)
		}
	}
}

func TestChunnelSettingsPersistReloadExportAndImport(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "themes")
	settingsDir := t.TempDir()
	manager := NewManager(root, "chunnel", WithSettingsDir(settingsDir))
	if err := manager.Load(); err != nil {
		t.Fatalf("load chunnel settings: %v", err)
	}

	initial := manager.SettingsSnapshot()
	if initial.Theme != "chunnel" || initial.ThemeVersion != manager.Active().Version {
		t.Fatalf("unexpected settings metadata: %#v", initial)
	}
	declaredJSON, err := os.ReadFile(filepath.Join(root, "chunnel", manager.Active().SettingsSchema))
	if err != nil {
		t.Fatal(err)
	}
	var declared struct {
		Properties map[string]json.RawMessage `json:"properties"`
		UI         struct {
			Groups []struct {
				ID string `json:"id"`
			} `json:"groups"`
		} `json:"x-newpress"`
	}
	if err := json.Unmarshal(declaredJSON, &declared); err != nil {
		t.Fatal(err)
	}
	if len(declared.Properties) == 0 || len(initial.Values) != len(declared.Properties) {
		t.Fatalf("settings fields = %d, schema declares %d", len(initial.Values), len(declared.Properties))
	}
	for key := range declared.Properties {
		if _, ok := initial.Values[key]; !ok {
			t.Fatalf("settings snapshot is missing declared field %q", key)
		}
	}
	if len(declared.UI.Groups) == 0 || len(initial.Groups) != len(declared.UI.Groups) {
		t.Fatalf("settings groups = %d, schema declares %d", len(initial.Groups), len(declared.UI.Groups))
	}
	for _, expected := range declared.UI.Groups {
		found := false
		for _, group := range initial.Groups {
			found = found || group.ID == expected.ID
		}
		if !found {
			t.Fatalf("settings snapshot is missing declared group %q", expected.ID)
		}
	}
	if initial.Values["cookie_consent_enabled"] != true || initial.Values["cookie_privacy_url"] != "" {
		t.Fatalf("cookie consent defaults = %#v, %#v", initial.Values["cookie_consent_enabled"], initial.Values["cookie_privacy_url"])
	}
	for _, group := range initial.Groups {
		if group.ID == "home_custom" {
			t.Fatal("custom HTML must be edited in the layout card, not a separate settings group")
		}
	}
	initial.Values["accent"] = "#000000"
	if got := manager.SettingsSnapshot().Values["accent"]; got != "#2563eb" {
		t.Fatalf("snapshot mutation changed manager state: %v", got)
	}

	values := manager.SettingsSnapshot().Values
	values["accent"] = "#ABCDEF"
	values["content_width"] = int64(1280)
	values["font_scale"] = 1.1
	values["heading_style"] = "modern"
	values["visual_style"] = "liquid-glass"
	values["feed_columns"] = "grid"
	values["sticky_header"] = false
	values["reduced_motion"] = true
	values["article_copyright_text"] = "{author} 的原创文章，转载请注明 {url}。"
	values["tip_hide_amount"] = true
	saved, fieldErrors, err := manager.SaveSettings(values)
	if err != nil || len(fieldErrors) != 0 {
		t.Fatalf("save settings = %#v, %v", fieldErrors, err)
	}
	if saved.Revision == initial.Revision || saved.UpdatedAt.IsZero() {
		t.Fatalf("save did not update revision/time: %#v", saved)
	}
	if _, err := os.Stat(filepath.Join(settingsDir, "chunnel.json")); err != nil {
		t.Fatalf("settings file was not persisted: %v", err)
	}

	css := manager.SettingsCSS()
	for _, marker := range []string{"--cyan:#abcdef;", "--shell-max:1280px;", "--font-scale:1.1;"} {
		if !strings.Contains(css, marker) {
			t.Fatalf("settings CSS is missing %q:\n%s", marker, css)
		}
	}
	classes := strings.Fields(manager.SettingsBodyClasses())
	for _, expected := range []string{"theme-feed-grid", "theme-force-reduced-motion", "theme-header-static", "theme-headings-modern"} {
		if !containsString(classes, expected) {
			t.Fatalf("body classes %q are missing %q", classes, expected)
		}
	}
	if containsString(classes, "theme-header-sticky") {
		t.Fatalf("body classes retained disabled sticky state: %q", classes)
	}

	reloaded := NewManager(root, "chunnel", WithSettingsDir(settingsDir))
	if err := reloaded.Load(); err != nil {
		t.Fatalf("reload persisted settings: %v", err)
	}
	reloadedSnapshot := reloaded.SettingsSnapshot()
	if reloadedSnapshot.Revision != saved.Revision || reloadedSnapshot.Values["accent"] != "#ABCDEF" || reloadedSnapshot.Values["visual_style"] != "liquid-glass" {
		t.Fatalf("reloaded settings differ: %#v", reloadedSnapshot)
	}
	if reloadedSnapshot.Values["article_copyright_text"] != values["article_copyright_text"] || reloadedSnapshot.Values["tip_hide_amount"] != true {
		t.Fatal("copyright or tip privacy settings were not persisted")
	}

	var exported bytes.Buffer
	if err := reloaded.ExportSettings(&exported); err != nil {
		t.Fatalf("export settings: %v", err)
	}
	var document settingsDocument
	if err := json.Unmarshal(exported.Bytes(), &document); err != nil {
		t.Fatalf("decode exported settings: %v", err)
	}
	if document.Schema != settingsDocumentSchema || document.Theme != "chunnel" || document.Values["accent"] != "#ABCDEF" {
		t.Fatalf("unexpected exported document: %#v", document)
	}

	if _, err := reloaded.ResetSettings(); err != nil {
		t.Fatalf("reset settings: %v", err)
	}
	if got := reloaded.SettingsSnapshot().Values["accent"]; got != "#2563eb" {
		t.Fatalf("reset accent = %v", got)
	}
	if got := reloaded.SettingsSnapshot().Values["visual_style"]; got != "classic" {
		t.Fatalf("reset visual style = %v", got)
	}
	imported, importErrors, err := reloaded.ImportSettings(bytes.NewReader(exported.Bytes()))
	if err != nil || len(importErrors) != 0 {
		t.Fatalf("import settings = %#v, %v", importErrors, err)
	}
	if imported.Values["accent"] != "#ABCDEF" || imported.Values["visual_style"] != "liquid-glass" || imported.Revision != saved.Revision {
		t.Fatalf("imported settings differ: %#v", imported)
	}

}

func TestCookiePrivacyURLRequiresSafeLocalPath(t *testing.T) {
	t.Parallel()
	manager := NewManager(filepath.Join("..", "..", "themes"), "chunnel", WithSettingsDir(t.TempDir()))
	if err := manager.Load(); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "/privacy/", "/pages/privacy-policy/", "/privacy/?lang=zh#cookies"} {
		values := manager.SettingsSnapshot().Values
		values["cookie_privacy_url"] = value
		if _, fields, err := manager.SaveSettings(values); err != nil || len(fields) != 0 {
			t.Errorf("valid privacy path %q: %v %v", value, fields, err)
		}
	}
	for _, value := range []string{"javascript:alert(1)", "https://outside.test/privacy", "//outside.test/privacy", `/\outside.test`, "/privacy/with space", "/privacy/\nredirect"} {
		values := manager.SettingsSnapshot().Values
		values["cookie_privacy_url"] = value
		if _, fields, _ := manager.SaveSettings(values); fields["cookie_privacy_url"] == "" {
			t.Errorf("unsafe privacy path accepted: %q", value)
		}
	}
}

func TestChunnelSettingsRejectInvalidValuesAndDocuments(t *testing.T) {
	t.Parallel()
	manager := NewManager(filepath.Join("..", "..", "themes"), "chunnel", WithSettingsDir(t.TempDir()))
	if err := manager.Load(); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"unknown field": func(values map[string]any) { values["custom_script"] = "alert(1)" },
		"invalid color": func(values map[string]any) { values["accent"] = "red;body{}" },
		"out of range":  func(values map[string]any) { values["content_width"] = int64(9000) },
		"wrong type":    func(values map[string]any) { values["sticky_header"] = "yes" },
		"invalid enum":  func(values map[string]any) { values["feed_columns"] = "masonry" },
		"invalid style": func(values map[string]any) { values["visual_style"] = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			values := manager.SettingsSnapshot().Values
			mutate(values)
			_, fieldErrors, err := manager.SaveSettings(values)
			if err != nil {
				t.Fatalf("validation returned storage error: %v", err)
			}
			if len(fieldErrors) == 0 {
				t.Fatal("invalid settings were accepted")
			}
		})
	}

	valid := manager.SettingsSnapshot().Values
	payload, err := json.Marshal(settingsDocument{Schema: settingsDocumentSchema, Theme: "chunnel", Values: valid})
	if err != nil {
		t.Fatal(err)
	}
	for name, document := range map[string]string{
		"wrong theme":    `{"schema":1,"theme":"other","values":{}}`,
		"unknown member": `{"schema":1,"theme":"chunnel","values":{},"script":"x"}`,
		"trailing value": string(payload) + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := manager.ImportSettings(strings.NewReader(document)); err == nil {
				t.Fatal("invalid import document was accepted")
			}
		})
	}
	oversized := strings.Repeat(" ", maxSettingsDocumentBytes+1)
	if _, _, err := manager.ImportSettings(strings.NewReader(oversized)); err == nil {
		t.Fatal("oversized settings import was accepted")
	}
}

func TestThemeSettingsRejectOversizedSavedDocumentWithoutChangingState(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	properties := make(map[string]settingProperty, 200)
	baselineValues := make(map[string]any, 200)
	oversizedValues := make(map[string]any, 200)
	for index := 0; index < 200; index++ {
		key := fmt.Sprintf("field_%03d", index)
		properties[key] = settingProperty{Type: "string", Default: "saved"}
		baselineValues[key] = "saved"
		oversizedValues[key] = strings.Repeat("x", 4096)
	}
	manager := &Manager{
		settingsDir:    directory,
		info:           Manifest{Slug: "bounded", Version: "1.0.0"},
		settingsSchema: &settingSchema{Type: "object", Properties: properties},
		settings:       baselineValues,
	}

	baseline, fieldErrors, err := manager.SaveSettings(baselineValues)
	if err != nil || len(fieldErrors) != 0 {
		t.Fatalf("save baseline settings = %#v, %v", fieldErrors, err)
	}
	path := settingsFilePath(directory, "bounded")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, fieldErrors, err := manager.SaveSettings(oversizedValues); err == nil || len(fieldErrors) != 0 {
		t.Fatalf("oversized save = %#v, %v; want storage error", fieldErrors, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("oversized save changed the persisted settings document")
	}
	current := manager.SettingsSnapshot()
	if current.Revision != baseline.Revision || current.Values["field_000"] != "saved" {
		t.Fatalf("oversized save changed in-memory settings: %#v", current)
	}
	temporary, err := filepath.Glob(filepath.Join(directory, ".settings-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("oversized save left temporary files: %v", temporary)
	}
}

func TestDecodeJSONDocumentRequiresExactlyOneBoundedValue(t *testing.T) {
	t.Parallel()
	var value map[string]any
	if err := decodeJSONDocument(strings.NewReader(`{"ok":true}`), 64, &value, true); err != nil {
		t.Fatalf("decode valid document: %v", err)
	}
	if value["ok"] != true {
		t.Fatalf("decoded value = %#v", value)
	}
	for _, input := range []string{
		`{"ok":true}{}`,
		`{"ok":true} junk`,
		`{"ok":true,"ok":false}`,
		`{"nested":{"value":1,"value":2}}`,
		``,
		strings.Repeat("x", 65),
	} {
		if err := decodeJSONDocument(strings.NewReader(input), 64, &value, true); err == nil {
			t.Fatalf("accepted invalid document %q", input)
		}
	}
}

func TestThemeSettingsRecoverMissingPrimaryFromRollbackCopy(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "themes")
	settingsDir := t.TempDir()
	manager := NewManager(root, "chunnel", WithSettingsDir(settingsDir))
	if err := manager.Load(); err != nil {
		t.Fatal(err)
	}
	values := manager.SettingsSnapshot().Values
	values["accent"] = "#123456"
	saved, fieldErrors, err := manager.SaveSettings(values)
	if err != nil || len(fieldErrors) != 0 {
		t.Fatalf("save settings = %#v, %v", fieldErrors, err)
	}
	path := settingsFilePath(settingsDir, "chunnel")
	if err := os.Rename(path, path+".bak"); err != nil {
		t.Fatal(err)
	}
	recovered := NewManager(root, "chunnel", WithSettingsDir(settingsDir))
	if err := recovered.Load(); err != nil {
		t.Fatalf("recover theme settings from rollback copy: %v", err)
	}
	snapshot := recovered.SettingsSnapshot()
	if snapshot.Revision != saved.Revision || snapshot.Values["accent"] != "#123456" {
		t.Fatalf("recovered settings = %#v", snapshot)
	}

	// A corrupt primary must not be hidden by an older rollback copy.
	if err := os.WriteFile(path, []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewManager(root, "chunnel", WithSettingsDir(settingsDir)).Load(); err == nil {
		t.Fatal("corrupt primary theme settings fell back to rollback copy")
	}
}

func TestThemeSettingsSchemaRejectsUnsafeDirectives(t *testing.T) {
	t.Parallel()
	newSchema := func() *settingSchema {
		return &settingSchema{
			Type:     "object",
			Required: []string{"accent"},
			UI:       schemaUI{Version: 1, Groups: []groupSpec{{ID: "brand"}}},
			Properties: map[string]settingProperty{"accent": {
				Type: "string", Default: "safe", UI: fieldUI{Group: "brand", Control: "text"},
			}},
		}
	}
	for name, mutate := range map[string]func(*settingSchema){
		"additional properties": func(schema *settingSchema) { schema.AdditionalProperties = true },
		"unknown required":      func(schema *settingSchema) { schema.Required = append(schema.Required, "missing") },
		"unsupported UI version": func(schema *settingSchema) {
			schema.UI.Version = 99
		},
		"unsupported control": func(schema *settingSchema) {
			property := schema.Properties["accent"]
			property.UI.Control = "code-editor"
			schema.Properties["accent"] = property
		},
		"unsafe CSS unit": func(schema *settingSchema) {
			property := schema.Properties["accent"]
			property.UI.CSSUnit = ";color:red"
			schema.Properties["accent"] = property
		},
		"unsafe CSS mapping": func(schema *settingSchema) {
			property := schema.Properties["accent"]
			property.UI.CSSMap = map[string]string{"safe": "red;display:none"}
			schema.Properties["accent"] = property
		},
		"oversized length constraint": func(schema *settingSchema) {
			property := schema.Properties["accent"]
			maximum := 4097
			property.MaxLength = &maximum
			schema.Properties["accent"] = property
		},
		"too many choices": func(schema *settingSchema) {
			property := schema.Properties["accent"]
			property.Enum = make([]any, 257)
			schema.Properties["accent"] = property
		},
		"unsupported format": func(schema *settingSchema) {
			property := schema.Properties["accent"]
			property.Format = "html"
			schema.Properties["accent"] = property
		},
	} {
		t.Run(name, func(t *testing.T) {
			schema := newSchema()
			mutate(schema)
			if err := validateSettingSchema(schema); err == nil {
				t.Fatal("unsafe schema directive was accepted")
			}
		})
	}

	uriProperty := settingProperty{Type: "string", Format: "uri-reference"}
	for _, unsafe := range []string{"JaVaScRiPt:alert(1)", "DATA:text/html,test", "vbscript:msgbox(1)", "https://example.test/\nheader"} {
		if _, err := normalizeSettingValue("link", uriProperty, unsafe); err == nil {
			t.Fatalf("unsafe URI %q was accepted", unsafe)
		}
	}
}

func TestThemeSettingIntegerConversionRejectsOverflow(t *testing.T) {
	t.Parallel()
	property := settingProperty{Type: "integer"}
	for _, test := range []struct {
		value any
		want  int64
	}{
		{value: json.Number("9223372036854775807"), want: math.MaxInt64},
		{value: int64(math.MaxInt64), want: math.MaxInt64},
		{value: int64(math.MinInt64), want: math.MinInt64},
	} {
		got, err := normalizeSettingValue("count", property, test.value)
		if err != nil || got != test.want {
			t.Fatalf("normalize integer %v = %v, %v", test.value, got, err)
		}
	}
	for _, value := range []any{
		json.Number("9223372036854775808"),
		json.Number("-9223372036854775809"),
		json.Number("1.5"),
		float64(math.MaxInt64),
		math.Inf(1),
	} {
		if _, err := normalizeSettingValue("count", property, value); err == nil {
			t.Fatalf("accepted invalid integer %v", value)
		}
	}
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
