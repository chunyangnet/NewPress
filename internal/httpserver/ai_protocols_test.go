package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/aieditor"
)

func TestAISettingsNativeProtocolGeneration(t *testing.T) {
	s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": principal.CSRFToken}
	for _, tc := range []struct{ protocol, auth, response string }{
		{aieditor.ProtocolResponses, "Authorization", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"原生协议正文。"}]}]}`},
		{aieditor.ProtocolAnthropic, "x-api-key", `{"type":"message","content":[{"type":"text","text":"原生协议正文。"}],"stop_reason":"end_turn"}`},
		{aieditor.ProtocolGeminiText, "x-goog-api-key", `{"candidates":[{"content":{"parts":[{"text":"原生协议正文。"}]},"finishReason":"STOP"}]}`},
		{aieditor.ProtocolAzure, "api-key", `{"choices":[{"message":{"content":"原生协议正文。"},"finish_reason":"stop"}]}`},
		{aieditor.ProtocolAzureResponses, "api-key", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"原生协议正文。"}]}]}`},
		{aieditor.ProtocolAzureLegacy, "api-key", `{"choices":[{"message":{"content":"原生协议正文。"},"finish_reason":"stop"}]}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			settings := s.aiSettings.Snapshot()
			settings.DefaultProvider = tc.protocol
			settings.Providers = append(settings.Providers, aieditor.Provider{ID: tc.protocol, Name: tc.protocol, Protocol: tc.protocol, BaseURL: "https://provider.example.test", APIKey: "synthetic-private-key", Models: []string{"writer-model"}})
			encoded, _ := json.Marshal(settings)
			saved := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/settings", string(encoded), headers)
			if saved.Code != 200 || strings.Contains(saved.Body.String(), "synthetic-private-key") {
				t.Fatalf("save: %d %s", saved.Code, saved.Body.String())
			}
			connections := serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/admin/ai/connections", "", nil)
			if connections.Code != 200 || !strings.Contains(connections.Body.String(), tc.protocol) || strings.Contains(connections.Body.String(), "synthetic-private-key") {
				t.Fatal("connection unavailable or unredacted")
			}
			calls := 0
			setAIResponse(s, 200, "application/json", tc.response, func(r *http.Request) {
				calls++
				want := "synthetic-private-key"
				if tc.auth == "Authorization" {
					want = "Bearer " + want
				}
				if r.Header.Get(tc.auth) != want {
					t.Error("saved native protocol lost its authentication")
				}
			})
			body := `{"provider_id":"` + tc.protocol + `","model":"writer-model","prompt_id":"article","brief":"写作主题","tone":"natural","length":"short","language":"zh-CN","writing_style":"restrained"}`
			response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/ai/generate", body, headers)
			if response.Code != 200 || calls != 1 || !strings.Contains(response.Body.String(), "event: done") || !strings.Contains(response.Body.String(), `"complete":true`) || strings.Contains(response.Body.String(), "synthetic-private-key") {
				t.Fatalf("generation: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
