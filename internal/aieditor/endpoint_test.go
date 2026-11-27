package aieditor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovedEndpointKeylessModelsAndGeneration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key"} {
			if _, exists := r.Header[name]; exists {
				t.Errorf("empty key emitted auth header %s", name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
		case "/v1/chat/completions":
			w.Write([]byte(`{"choices":[{"message":{"content":"本地模型正文。"},"finish_reason":"stop"}]}`))
		case "/v1/images/generations":
			// A valid 1x1 PNG; local images stay inline instead of widening the downloader.
			png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRz8AAAAASUVORK5CYII="
			w.Write([]byte(`{"data":[{"b64_json":"` + png + `"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "ai-settings.json")
	store, _ := NewSettingsStore(path)
	settings := store.Snapshot()
	settings.DefaultProvider = "local"
	settings.Providers = []Provider{{ID: "local", Name: "本地", BaseURL: server.URL, AllowInsecure: true, Models: []string{"local-model"}}}
	saved, err := store.Save(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Providers[0].AllowInsecure || saved.Providers[0].HasKey {
		t.Fatal("approval or keyless state lost")
	}
	reopened, err := NewSettingsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := reopened.Resolve(Connection{ProviderID: "local"}, true)
	if err != nil {
		t.Fatal(err)
	}
	models, err := NewClient().Models(context.Background(), connection)
	if err != nil || len(models) != 1 {
		t.Fatal("local discovery", models, err)
	}
	result, err := NewClient().Stream(context.Background(), connection, []Message{{Role: "user", Content: "写作"}}, 0, func(string) error { return nil })
	if err != nil || result.Text != "本地模型正文。" {
		t.Fatal("local generation", err)
	}
	imageConnection, err := ApproveProbeConnection(Connection{Protocol: ProtocolOpenAIImages, BaseURL: server.URL, Model: "local-image"})
	if err != nil {
		t.Fatal(err)
	}
	image, err := NewImageClient().Generate(context.Background(), imageConnection, ImageGeneration{Prompt: "画一张图片", AspectRatio: "1:1"})
	if err != nil || len(image) != 1 || len(image[0].Data) == 0 {
		t.Fatal("local inline image", err)
	}
	connection.BaseURL = server.URL + "/other"
	if _, err := connection.Validate(true); err == nil {
		t.Fatal("approval reused for new path")
	}
	connection.BaseURL = "http://localhost:1234/v1"
	if _, err := connection.Validate(true); err == nil {
		t.Fatal("approval reused for new host")
	}
	if _, err := NewClient().Models(context.Background(), Connection{BaseURL: server.URL, APIKey: "key"}); err == nil {
		t.Fatal("temporary unapproved HTTP accepted")
	}
	saved.Providers[0].AllowInsecure = false
	if _, err := reopened.Save(saved); err == nil {
		t.Fatal("HTTP continued after opt-out")
	}
}

func TestApprovedEndpointCredentialPreservationAndJSONIsolation(t *testing.T) {
	store, _ := NewSettingsStore("")
	settings := Settings{Revision: store.Snapshot().Revision, DefaultProvider: "local", Providers: []Provider{{ID: "local", Name: "本地", BaseURL: "http://127.0.0.1:11434/v1", AllowInsecure: true, APIKey: "private-test-key", Models: []string{"model"}}}}
	saved, err := store.Save(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(saved); err != nil {
		t.Fatal("preserving saved key", err)
	}
	connection, _ := store.Resolve(Connection{ProviderID: "local"}, true)
	if connection.APIKey != "private-test-key" {
		t.Fatal("saved key lost")
	}
	for _, change := range []func(*Provider){func(p *Provider) { p.BaseURL = "http://localhost:1234/v1" }, func(p *Provider) { p.Protocol = ProtocolResponses }} {
		input := store.Snapshot()
		change(&input.Providers[0])
		if _, err := store.Save(input); err == nil {
			t.Fatal("forwarded saved key to changed target")
		}
	}
	data, _ := json.Marshal(connection)
	if strings.Contains(string(data), "approved") || strings.Contains(string(data), "allow_insecure") {
		t.Fatal("internal authorization serialized")
	}
	var copied Connection
	json.Unmarshal(data, &copied)
	if _, err := copied.Validate(true); err == nil {
		t.Fatal("JSON roundtrip preserved internal trust")
	}
	var forged Connection
	json.Unmarshal([]byte(`{"base_url":"http://localhost:1234","api_key":"key","allow_insecure":true,"approvedEndpoint":"http://localhost:1234/v1"}`), &forged)
	if _, err := forged.Validate(false); err == nil {
		t.Fatal("JSON granted endpoint access")
	}
	if _, err := ApproveProbeConnection(Connection{ProviderID: "local", BaseURL: "http://localhost:1234"}); err == nil {
		t.Fatal("mixed saved and temporary trust accepted")
	}
	input := store.Snapshot()
	input.Providers[0].BaseURL = "https://provider.example/v1"
	input.Providers[0].APIKey = "replacement-key"
	input.Providers[0].AllowInsecure = false
	if _, err := store.Save(input); err != nil {
		t.Fatal("revoke with valid default endpoint", err)
	}
	resolved, err := store.Resolve(Connection{ProviderID: "local"}, true)
	if err != nil || resolved.approvedEndpoint != "" {
		t.Fatal("trust not revoked", err)
	}
}
