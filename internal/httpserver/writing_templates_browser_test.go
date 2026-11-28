package httpserver

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/writingtemplate"
)

// A disposable real-router fixture. No model service or production account is used.
func TestWritingTemplatesBrowserFixture(t *testing.T) {
	folder := os.Getenv("NEWPRESS_WRITING_TEMPLATES_BROWSER")
	if folder == "" {
		t.Skip("opt-in writing templates browser fixture")
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	s, _, _, _, cookie, owner := newPersistentAdminTestServer(t)
	storage, err := writingtemplate.NewFileStorage(filepath.Join(t.TempDir(), "writing-templates.json"))
	if err != nil {
		t.Fatal(err)
	}
	library, err := writingtemplate.New(t.Context(), storage)
	if err != nil {
		t.Fatal(err)
	}
	s.SetWritingTemplates(library)
	settings, err := aieditor.NewSettingsStore(filepath.Join(t.TempDir(), "ai-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	data := settings.Snapshot()
	data.DefaultProvider = "template-writer"
	data.Providers = []aieditor.Provider{{ID: "template-writer", Name: "本机模拟写作", BaseURL: "https://fixture.example.test/v1", APIKey: "synthetic-only", Models: []string{"template-test"}, DefaultModel: "template-test"}}
	if _, err = settings.Save(data); err != nil {
		t.Fatal(err)
	}
	s.SetAISettings(settings)
	setAIResponse(s, 200, "text/event-stream", aiFrame("<newpress-meta>{\"title\":\"版本化写作结果\"}</newpress-meta>\n\n保留材料里的事实。", "stop")+"data: [DONE]\n\n", nil)
	server := httptest.NewServer(s.routes())
	defer server.Close()
	s.config.PublicURL = server.URL
	raw, _ := json.Marshal(map[string]any{"url": server.URL, "cookie_name": cookie.Name, "cookie_value": cookie.Value, "csrf_token": owner.CSRFToken})
	if err = os.WriteFile(filepath.Join(folder, "connection.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(12 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(folder, "stop")); err == nil {
				return
			}
		case <-timer.C:
			t.Fatal("browser fixture was not stopped")
		case <-t.Context().Done():
			return
		}
	}
}
