package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"newpress.local/newpress/internal/commentorigin"
	"newpress.local/newpress/internal/plugin"
)

func TestCommentOriginHTTPRejectsUnreadableSettings(t *testing.T) {
	for _, retained := range []bool{false, true} {
		name := "json_escaping"
		if retained {
			name = "retained_key"
		}
		t.Run(name, func(t *testing.T) {
			s, handler, _, cookie, principal := newCommentHTTPTestServer(t)
			if err := s.plugins.InstallBuiltin(); err != nil {
				t.Fatal(err)
			}
			write := func(settings commentorigin.Settings) *httptest.ResponseRecorder {
				t.Helper()
				var payload bytes.Buffer
				encoder := json.NewEncoder(&payload)
				encoder.SetEscapeHTML(false)
				if err := encoder.Encode(map[string]any{"settings": settings, "expected_user_id": principal.UserID}); err != nil {
					t.Fatal(err)
				}
				if payload.Len() >= 32<<10 {
					t.Fatalf("fixture must pass request size limit: %d bytes", payload.Len())
				}
				t.Logf("HTTP request: %d bytes", payload.Len())
				return serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPut, "https://example.test/api/v1/admin/plugins/comment-origin/settings", payload.String(), "", "https://example.test")
			}
			initial := commentorigin.Defaults()
			initial.Provider = "baidu"
			initial.Key = "http-key-sentinel" + strings.Repeat("&", 480)
			if response := write(initial); response.Code != http.StatusOK {
				t.Fatalf("initial save: %d %s", response.Code, response.Body.String())
			}
			baseline, err := s.commentOrigin.Settings()
			if err != nil {
				t.Fatal(err)
			}
			var path string
			if err := s.plugins.WithData(plugin.CommentOrigin, false, 0, func(dir string, _ uint64) error {
				path = filepath.Join(dir, "settings.json")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			candidate := baseline
			candidate.Key = ""
			if retained {
				encoded, err := json.Marshal(candidate)
				if err != nil {
					t.Fatal(err)
				}
				candidate.Endpoint = strings.Repeat("x", (32<<10)-512-len(encoded))
			} else {
				candidate.Endpoint = strings.Repeat("&", 6000)
			}
			response := write(candidate)
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "32 KiB") {
				t.Errorf("expected clear size rejection: status=%d response_bytes=%d", response.Code, response.Body.Len())
			}
			if strings.Contains(response.Body.String(), "http-key-sentinel") {
				t.Error("response disclosed retained key")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, original) {
				t.Errorf("rejected request replaced settings: bytes=%d err=%v", len(after), err)
			}
			s.commentOrigin = commentorigin.New(s.plugins)
			reloaded, err := s.commentOrigin.Settings()
			if err != nil {
				t.Fatalf("reload after rejection: %v", err)
			}
			if !reflect.DeepEqual(reloaded, baseline) {
				t.Error("rejected request changed key, settings or version")
			}
			candidate.Endpoint = "https://geo.example.test/?ip={ip}"
			response = write(candidate)
			if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "http-key-sentinel") {
				t.Fatalf("valid retry failed or disclosed key: status=%d", response.Code)
			}
			status, err := s.commentOrigin.Status()
			if err != nil || !status.KeySaved || status.Settings.Key != "" || status.Settings.Version != baseline.Version+1 {
				t.Errorf("valid retry did not preserve key and advance version: %v", err)
			}
		})
	}
}
