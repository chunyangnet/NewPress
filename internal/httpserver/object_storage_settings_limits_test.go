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

	"newpress.local/newpress/internal/objectstorage"
	"newpress.local/newpress/internal/plugin"
)

func TestObjectStorageHTTPRejectsUnreadableSettings(t *testing.T) {
	for _, retained := range []bool{false, true} {
		name := "json_escaping"
		if retained {
			name = "retained_credentials"
		}
		t.Run(name, func(t *testing.T) {
			s, handler, cookie, principal := newStorageHTTPTestServer(t)
			write := func(c objectstorage.Settings) *httptest.ResponseRecorder {
				t.Helper()
				var payload bytes.Buffer
				encoder := json.NewEncoder(&payload)
				// Browser JSON.stringify sends these characters literally.
				encoder.SetEscapeHTML(false)
				if err := encoder.Encode(map[string]any{"settings": c, "expected_user_id": principal.UserID}); err != nil {
					t.Fatal(err)
				}
				if payload.Len() >= 128<<10 {
					t.Fatalf("fixture must pass the HTTP request limit: %d bytes", payload.Len())
				}
				t.Logf("HTTP request: %d bytes", payload.Len())
				return serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPut, "https://example.test/api/v1/admin/plugins/object-storage/settings", payload.String(), "", "https://example.test")
			}
			initial := storageHTTPSettings()
			if retained {
				initial.Profiles[0].SecretKey = strings.Repeat("<", 8192)
				initial.Profiles[0].SessionToken = ""
				second := initial.Profiles[0]
				second.ID = "second"
				initial.Profiles = append(initial.Profiles, second)
			}
			if r := write(initial); r.Code != http.StatusOK {
				t.Fatalf("initial save: %d %s", r.Code, r.Body.String())
			}
			baseline, err := s.objectStorage.Status()
			if err != nil {
				t.Fatal(err)
			}
			var path string
			if err := s.plugins.WithData(plugin.ObjectStorage, false, 0, func(dir string, _ uint64) error {
				path = filepath.Join(dir, "settings.json")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			candidate := baseline.Settings.Settings
			candidate.Profiles = append([]objectstorage.Profile(nil), candidate.Profiles...)
			if retained {
				third := initial.Profiles[0]
				third.ID = "third"
				candidate.Profiles = append(candidate.Profiles, third)
			} else {
				candidate.Profiles[0].AccessKey = strings.Repeat(">", 8192)
				candidate.Profiles[0].SecretKey = strings.Repeat("<", 8192)
				candidate.Profiles[0].SessionToken = strings.Repeat("&", 8192)
			}
			response := write(candidate)
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "128 KiB") {
				t.Errorf("expected explicit size rejection: status=%d response_bytes=%d", response.Code, response.Body.Len())
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Errorf("rejected HTTP save replaced settings: bytes=%d err=%v", len(after), err)
			}
			s.objectStorage.Close()
			s.SetObjectStorage(objectstorage.New(s.plugins, s.media))
			t.Cleanup(s.objectStorage.Close)
			restarted, err := s.objectStorage.Status()
			if err != nil {
				t.Fatalf("restart after rejected save: %v", err)
			}
			if !reflect.DeepEqual(restarted.Settings, baseline.Settings) {
				t.Fatal("rejected save changed settings or version")
			}
			correction := restarted.Settings.Settings
			correction.Profiles[0].Name = "保存修正后的配置"
			if r := write(correction); r.Code != http.StatusOK {
				t.Fatalf("valid retry after rejected save: %d %s", r.Code, r.Body.String())
			}
		})
	}
}
