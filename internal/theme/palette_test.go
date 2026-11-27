package theme

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestChunnelSavedNeutralPaletteIsPreserved(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, version, paper, ink, highlight, wantPaper, wantInk, wantHighlight string
	}{
		{"launch defaults", "0.45.0", "#faf9f5", "#141413", "#e8a55a", "#f7f9fc", "#17253d", "#3b82f6"},
		{"blue defaults", "0.45.1", "#FAF9F5", "#141413", "#E8A55A", "#f7f9fc", "#17253d", "#3b82f6"},
		{"unversioned", "", "#faf9f5", "#141413", "#e8a55a", "#f7f9fc", "#17253d", "#3b82f6"},
		{"custom palette", "0.45.1", "#ffffff", "#112233", "#aabbcc", "#ffffff", "#112233", "#aabbcc"},
		{"custom paper", "0.45.1", "#ffffff", "#141413", "#e8a55a", "#ffffff", "#17253d", "#3b82f6"},
		{"new intentional warm palette", "0.45.2", "#faf9f5", "#141413", "#e8a55a", "#faf9f5", "#141413", "#e8a55a"},
		{"future palette", "0.46.0", "#faf9f5", "#141413", "#e8a55a", "#faf9f5", "#141413", "#e8a55a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			document := settingsDocument{Schema: settingsDocumentSchema, Theme: "chunnel", ThemeVersion: test.version,
				Values: map[string]any{"paper_color": test.paper, "ink_color": test.ink, "highlight_color": test.highlight, "accent": "#0f766e", "hero_title": "保留内容"}}
			if err := writeSettingsDocument(directory, document); err != nil {
				t.Fatal(err)
			}
			manager := NewManager(filepath.Join("..", "..", "themes"), "chunnel", WithSettingsDir(directory))
			if err := manager.Load(); err != nil {
				t.Fatal(err)
			}
			check := func(snapshot SettingsSnapshot) {
				t.Helper()
				for key, want := range map[string]string{"paper_color": test.paper, "ink_color": test.ink, "highlight_color": test.highlight, "accent": "#0f766e", "hero_title": "保留内容"} {
					if got := snapshot.Values[key]; got != want {
						t.Errorf("%s = %v, want %s", key, got, want)
					}
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
