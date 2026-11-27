package theme

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDocsHubRejectsRetiredSingletonBlocks(t *testing.T) {
	t.Parallel()
	manager := NewManager(filepath.Join("..", "..", "themes"), "docshub", WithSettingsDir(t.TempDir()))
	if err := manager.LoadExplicit("docshub"); err != nil {
		t.Fatal(err)
	}
	initial := manager.SettingsSnapshot()
	for _, key := range []string{"home_custom_title", "home_custom_html", "home_custom_surface", "home_custom_visibility"} {
		t.Run(key, func(t *testing.T) {
			if _, exists := initial.Values[key]; exists {
				t.Fatalf("retired setting %s remains in the current snapshot", key)
			}
			values := manager.SettingsSnapshot().Values
			values[key] = "retired"
			if _, _, _, fields := manager.PreviewSettings(values); fields[key] == "" {
				t.Fatalf("preview accepted retired setting %s: %v", key, fields)
			}
			payload, err := json.Marshal(settingsDocument{Schema: 1, Theme: "docshub", Values: values})
			if err != nil {
				t.Fatal(err)
			}
			if _, fields, err := manager.ImportSettings(bytes.NewReader(payload)); err != nil || fields[key] == "" {
				t.Fatalf("import did not reject retired setting %s as a field error: %v, %v", key, fields, err)
			}
		})
	}
	if current := manager.SettingsSnapshot(); !reflect.DeepEqual(current, initial) {
		t.Fatal("rejected retired settings changed the active theme settings")
	}
}

func TestNexoraCTAURLsUseCompletePaths(t *testing.T) {
	t.Parallel()
	manager := NewManager(filepath.Join("..", "..", "themes"), "nexora", WithSettingsDir(t.TempDir()))
	if err := manager.LoadExplicit("nexora"); err != nil {
		t.Fatal(err)
	}
	keys := []string{
		"contact_link", "hero_link", "second_link", "third_link", "about_link", "innovation_link",
		"business_1_link", "business_2_link", "business_3_link", "careers_link", "contact_action_url",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			for _, value := range []string{"", "/", "/#about", "/pages/contact/?lang=zh#form", "https://example.test/contact/", "http://example.test/"} {
				values := manager.SettingsSnapshot().Values
				values[key] = value
				preview, _, _, fields := manager.PreviewSettings(values)
				if len(fields) != 0 || preview.Values[key] != value {
					t.Fatalf("current URL %q rejected for %s: %v", value, key, fields)
				}
			}
			for _, value := range []string{"#about", "#contact", "pages/contact/", "//example.test/contact", "javascript:alert(1)", "/path with-space", `/\example.test`, `https://example.test\path`} {
				values := manager.SettingsSnapshot().Values
				values[key] = value
				if _, _, _, fields := manager.PreviewSettings(values); fields[key] == "" {
					t.Fatalf("invalid or retired URL %q accepted for %s: %v", value, key, fields)
				}
			}
		})
	}
}
