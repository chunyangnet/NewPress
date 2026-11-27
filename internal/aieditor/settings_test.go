package aieditor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSettings(revision string) Settings {
	return Settings{Revision: revision, DefaultProvider: "main", Providers: []Provider{
		{ID: "main", Name: "主力写作", BaseURL: "https://writer.example/v1/chat/completions", APIKey: "first-test-key", Models: []string{"model-a", "model-b", "model-a"}, DefaultModel: "model-b"},
		{ID: "backup", Name: "备用接口", BaseURL: "https://backup.example", APIKey: "second-test-key", Models: []string{"model-c"}},
	}}
}

func TestSettingsPersistenceRedactionAndResolution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "ai-settings.json")
	store, err := NewSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Save(testSettings(store.Snapshot().Revision))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(saved)
	if strings.Contains(string(encoded), "test-key") || strings.Contains(string(encoded), "api_key") || !saved.Providers[0].HasKey {
		t.Fatal("credentials leaked in snapshot")
	}
	if len(saved.Providers[0].Models) != 2 || saved.Providers[0].BaseURL != "https://writer.example/v1" {
		t.Fatal("normalization failed")
	}
	reopened, err := NewSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().Revision != saved.Revision {
		t.Fatal("revision did not survive restart")
	}
	for _, item := range []struct{ id, model, wantKey, wantModel string }{
		{"main", "", "first-test-key", "model-b"}, {"main", "model-a", "first-test-key", "model-a"}, {"backup", "", "second-test-key", "model-c"},
	} {
		connection, err := reopened.Resolve(Connection{ProviderID: item.id, Model: item.model}, true)
		if err != nil || connection.APIKey != item.wantKey || connection.Model != item.wantModel {
			t.Fatal("saved connection resolution failed")
		}
	}
	disabledUsage := false
	withoutUsage, err := reopened.Resolve(Connection{ProviderID: "main", StreamUsage: &disabledUsage}, true)
	if err != nil || withoutUsage.StreamUsage == nil || *withoutUsage.StreamUsage {
		t.Fatal("saved connection lost stream usage opt-out", err)
	}
	saved.Providers[0].Models[0] = "changed"
	if reopened.Snapshot().Providers[0].Models[0] != "model-a" {
		t.Fatal("snapshot mutated storage")
	}
	update := reopened.Snapshot()
	update.Providers[0].Name = "新名称"
	if _, err := reopened.Save(update); err != nil {
		t.Fatal("blank key must preserve existing credential", err)
	}
	connection, err := reopened.Resolve(Connection{ProviderID: "main"}, true)
	if err != nil || connection.APIKey != "first-test-key" {
		t.Fatal("blank key erased credential")
	}
	update = reopened.Snapshot()
	update.Providers[0].APIKey = "replacement-test-key"
	if _, err := reopened.Save(update); err != nil {
		t.Fatal(err)
	}
	connection, _ = reopened.Resolve(Connection{ProviderID: "main"}, true)
	if connection.APIKey != "replacement-test-key" {
		t.Fatal("credential rotation failed")
	}
}

func TestSettingsRejectStaleInvalidAndCredentialRedirects(t *testing.T) {
	store, _ := NewSettingsStore("")
	saved, err := store.Save(testSettings(store.Snapshot().Revision))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Settings){
		func(s *Settings) { s.Revision = "stale" },
		func(s *Settings) { s.Providers[0].BaseURL = "https://other.example" },
		func(s *Settings) { s.Providers[0].BaseURL = "https://127.0.0.1"; s.Providers[0].APIKey = "new-key" },
		func(s *Settings) { s.Providers[1].ID = "main" },
		func(s *Settings) { s.Providers[0].DefaultModel = "unlisted" },
		func(s *Settings) { s.Providers[0].Models = nil },
		func(s *Settings) { s.Providers[0].Models = []string{"bad model"} },
		func(s *Settings) { s.DefaultProvider = "missing" },
	} {
		input := store.Snapshot()
		change(&input)
		if _, err := store.Save(input); err == nil {
			t.Fatal("invalid update accepted")
		}
		if store.Snapshot().Revision != saved.Revision {
			t.Fatal("failed update changed persisted state")
		}
	}
	for _, connection := range []Connection{
		{ProviderID: "missing"}, {ProviderID: "main", Model: "model-c"},
		{ProviderID: "main", BaseURL: "https://attacker.example"}, {ProviderID: "main", APIKey: "override"},
	} {
		if _, err := store.Resolve(connection, true); err == nil {
			t.Fatal("invalid saved connection accepted")
		}
	}
	input := store.Snapshot()
	input.Providers = nil
	deleted, err := store.Save(input)
	if err != nil || len(deleted.Providers) != 0 || deleted.DefaultProvider != "" {
		t.Fatal("removal failed", err)
	}
	if _, err := store.Resolve(Connection{ProviderID: "main"}, true); err == nil {
		t.Fatal("removed key still usable")
	}
}

func TestSettingsWriteFailureAndCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-settings.json")
	store, _ := NewSettingsStore(path)
	initial := store.Snapshot()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(testSettings(initial.Revision)); err == nil {
		t.Fatal("save over directory succeeded")
	}
	if store.Snapshot().Revision != initial.Revision || len(store.Snapshot().Providers) != 0 {
		t.Fatal("failed write changed memory")
	}
	path = filepath.Join(t.TempDir(), "ai-settings.json")
	if err := os.WriteFile(path, []byte(`{"revision":"r","providers":[{"api_key":"sensitive-invalid-value"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSettingsStore(path); err == nil || strings.Contains(err.Error(), "sensitive-invalid-value") {
		t.Fatal("corrupt file silently accepted or secret exposed")
	}
}
