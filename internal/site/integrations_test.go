package site

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationsPersistImportAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	m, err := NewManager(path, DefaultSettings("Test", "Test"))
	if err != nil {
		t.Fatal(err)
	}
	initial := m.Snapshot()
	initialBytes, _ := os.ReadFile(path)
	if !bytes.Contains(initialBytes, []byte(`"integrations"`)) {
		t.Fatal("default integration settings are missing")
	}
	settings := initial.Settings
	settings.Integrations = &IntegrationsSettings{
		ICPNumber: " 京ICP备12345678号-1 ", PoliceNumber: "京公网安备11010102001234号", PoliceCode: "11010102001234",
		Analytics:    AnalyticsSettings{Enabled: true, GoogleID: "G-ABCDE12345", MatomoURL: "https://stats.example.com/matomo/", MatomoSiteID: "1", AllowedOrigins: " https://collect.example.com/ \nhttps://collect.example.com"},
		Verification: VerificationSettings{Google: "google-token", Custom: "example-site-verification = value=="},
	}
	saved, fields, err := m.Save(initial.Revision, settings)
	if err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	got := saved.Settings.IntegrationValues()
	if got.ICPNumber != "京ICP备12345678号-1" || got.Analytics.MatomoURL != "https://stats.example.com/matomo" || got.Analytics.AllowedOrigins != "https://collect.example.com" {
		t.Fatalf("normalization: %#v", got)
	}
	settings.Integrations.Analytics.GoogleID = "G-CHANGED123"
	saved.Settings.Integrations.Analytics.GoogleID = "G-CHANGED456"
	if m.Snapshot().Settings.IntegrationValues().Analytics.GoogleID != "G-ABCDE12345" {
		t.Fatal("mutable snapshot leaked")
	}
	loaded, err := NewManager(path, DefaultSettings("ignored", "ignored"))
	if err != nil || loaded.Snapshot().Settings.IntegrationValues() != got {
		t.Fatal("reload failed", err)
	}
	var exported bytes.Buffer
	if err := m.Export(&exported); err != nil {
		t.Fatal(err)
	}
	other, _ := NewManager("", DefaultSettings("Other", "Other"))
	imported, fields, err := other.Import(&exported, other.Snapshot().Revision)
	if err != nil || len(fields) != 0 || imported.Settings.IntegrationValues() != got {
		t.Fatal("import failed", fields, err)
	}
	clear := m.Snapshot()
	clear.Settings.Integrations = &IntegrationsSettings{}
	cleared, fields, err := m.Save(clear.Revision, clear.Settings)
	if err != nil || len(fields) != 0 || cleared.Revision != initial.Revision || cleared.Settings.Integrations == nil || cleared.Settings.IntegrationValues() != (IntegrationsSettings{}) {
		t.Fatal("clear did not restore explicit defaults", fields, err)
	}
	if _, err := NewManager(path, DefaultSettings("", "")); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationValidationRejectsExecutableAndMalformedInputs(t *testing.T) {
	cases := []struct{ key, value string }{
		{"analytics_google_id", `G-ABCD\";alert(1)`}, {"analytics_google_tag_id", "G-ABCD"},
		{"analytics_baidu_id", "<script>"}, {"analytics_clarity_id", "abc/../../x"},
		{"analytics_cloudflare_token", "bad-token"}, {"police_code", "123<script>"},
		{"analytics_umami_url", "javascript:alert(1)"}, {"analytics_plausible_url", "http://example.com/script.js"},
		{"analytics_plausible_url", "https://user:password@example.com/script.js"},
		{"analytics_plausible_url", "https://example.com/script.js#fragment"},
		{"analytics_plausible_url", "https://example.com/';script-src *"},
		{"analytics_matomo_url", "https://example.com/?query=x"},
		{"analytics_allowed_origins", "https://*.example.com"},
		{"analytics_allowed_origins", "https://example.com/path"},
		{"analytics_allowed_origins", "https://example.com; script-src *"},
		{"verification_custom", "refresh=0;url=https://example.com"},
		{"verification_custom", "<meta name=foo>"},
		{"verification_custom", "google-site-verification=duplicate"},
		{"verification_custom", "test-verification=a\ntest-verification=b"},
		{"verification_google", "line\nbreak"},
	}
	for _, tc := range cases {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			settings := DefaultSettings("", "")
			settings.Integrations = &IntegrationsSettings{PoliceNumber: "公网安备", PoliceCode: "11010102001234", Verification: VerificationSettings{Google: "original"}}
			*integrationTextFields(settings.Integrations)[tc.key] = tc.value
			_, fields := Validate(settings)
			if fields[tc.key] == "" {
				t.Fatalf("accepted %s=%q: %v", tc.key, tc.value, fields)
			}
		})
	}
	for _, settings := range []IntegrationsSettings{
		{PoliceNumber: "公网安备"}, {PoliceCode: "11010102001234"},
		{Analytics: AnalyticsSettings{UmamiURL: "https://example.com/script.js"}},
		{Analytics: AnalyticsSettings{MatomoSiteID: "1"}},
	} {
		base := DefaultSettings("", "")
		base.Integrations = &settings
		if _, fields := Validate(base); len(fields) == 0 {
			t.Fatal("accepted incomplete configuration")
		}
	}
}

func TestIntegrationsCannotBypassRevisionOnImport(t *testing.T) {
	settings := DefaultSettings("", "")
	doc := document{Schema: documentSchema, Kind: documentKind, Settings: settings, Revision: settingsRevision(settings)}
	payload, _ := json.Marshal(doc)
	var raw map[string]any
	_ = json.Unmarshal(payload, &raw)
	values := raw["settings"].(map[string]any)
	values["integrations"] = map[string]any{"analytics": map[string]any{"enabled": true, "clarity_id": "attack123"}}
	payload, _ = json.Marshal(raw)
	m, _ := NewManager("", settings)
	if _, _, err := m.Import(strings.NewReader(string(payload)), m.Snapshot().Revision); err == nil {
		t.Fatal("legacy hash ignored new integrations")
	}
}
