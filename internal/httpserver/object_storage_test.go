package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/objectstorage"
	"newpress.local/newpress/internal/plugin"
)

func newStorageHTTPTestServer(t *testing.T) (*Server, http.Handler, *http.Cookie, auth.Principal) {
	t.Helper()
	s, handler, _, cookie, principal := newCommentHTTPTestServer(t)
	if err := s.plugins.InstallBundled(plugin.ObjectStorage); err != nil {
		t.Fatal(err)
	}
	s.SetObjectStorage(objectstorage.New(s.plugins, s.media))
	return s, handler, cookie, principal
}

func storageHTTPSettings() objectstorage.Settings {
	c := objectstorage.Defaults()
	c.ActiveProfile = "archive"
	c.Profiles = []objectstorage.Profile{{ID: "archive", Name: "媒体归档", Provider: "s3-compatible", Endpoint: "https://storage.example.invalid", Region: "us-east-1", Bucket: "newpress-media", Prefix: "media", AccessKey: "storage-access-id", SecretKey: "storage-secret-sentinel", SessionToken: "storage-token-sentinel", PathStyle: true, TimeoutSeconds: 120}}
	return c
}

func TestObjectStorageHTTPSecretsAndOptimisticSave(t *testing.T) {
	s, handler, cookie, principal := newStorageHTTPTestServer(t)
	settings := storageHTTPSettings()
	write := func(c objectstorage.Settings) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(map[string]any{"settings": c, "expected_user_id": principal.UserID})
		return serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPut, "https://example.test/api/v1/admin/plugins/object-storage/settings", string(payload), "", "https://example.test")
	}
	response := write(settings)
	if response.Code != http.StatusOK {
		t.Fatalf("save: %d %s", response.Code, response.Body.String())
	}
	for _, secret := range []string{"storage-secret-sentinel", "storage-token-sentinel"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatal("save disclosed credential")
		}
	}
	var saved struct {
		Settings objectstorage.SettingsView `json:"settings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Settings.Version != 2 || len(saved.Settings.ProfileViews) != 1 || !saved.Settings.ProfileViews[0].SecretSaved || !saved.Settings.ProfileViews[0].TokenSaved {
		t.Fatalf("invalid redacted settings: %+v", saved.Settings)
	}
	if response = write(settings); response.Code != http.StatusConflict {
		t.Fatalf("stale save: %d %s", response.Code, response.Body.String())
	}
	next := saved.Settings.Settings
	next.Profiles[0].Name = "更新名称"
	if response = write(next); response.Code != http.StatusOK {
		t.Fatalf("retained blank secret: %d %s", response.Code, response.Body.String())
	}
	status, err := s.objectStorage.Status()
	if err != nil || status.Settings.Version != 3 || !status.Settings.ProfileViews[0].SecretSaved {
		t.Fatalf("retention state: %+v %v", status, err)
	}
	read := serveAdminCommentAPI(handler, cookie, "", http.MethodGet, "https://example.test/api/v1/admin/plugins/object-storage/status", "", "", "")
	if read.Code != 200 || read.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status: %d %s", read.Code, read.Body.String())
	}
	for _, secret := range []string{"storage-secret-sentinel", "storage-token-sentinel"} {
		if strings.Contains(read.Body.String(), secret) {
			t.Fatal("status disclosed credential")
		}
	}
	page := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/plugins/object-storage/", "", "", "8.8.8.8:80")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "data-storage-admin") || !strings.Contains(page.Body.String(), "Cloudreve") {
		t.Fatalf("storage page: %d %s", page.Code, page.Body.String())
	}
	for _, secret := range []string{"storage-secret-sentinel", "storage-token-sentinel"} {
		if strings.Contains(page.Body.String(), secret) {
			t.Fatal("page disclosed credential")
		}
	}
}

func TestObjectStorageHTTPRequiresSessionCSRFIdentityAndCleanupConfirmation(t *testing.T) {
	s, handler, cookie, principal := newStorageHTTPTestServer(t)
	settings := storageHTTPSettings()
	payload, _ := json.Marshal(map[string]any{"settings": settings, "expected_user_id": principal.UserID})
	for _, tc := range []struct {
		name, csrf, origin string
		cookie             *http.Cookie
		want               int
	}{
		{"no_session", principal.CSRFToken, "https://example.test", nil, 401},
		{"no_csrf", "", "https://example.test", cookie, 403},
		{"other_origin", principal.CSRFToken, "https://elsewhere.test", cookie, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "https://example.test/api/v1/admin/plugins/object-storage/settings", strings.NewReader(string(payload)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-CSRF-Token", tc.csrf)
			request.Header.Set("Origin", tc.origin)
			if tc.cookie != nil {
				request.AddCookie(tc.cookie)
			}
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != tc.want {
				t.Fatalf("got %d: %s", result.Code, result.Body.String())
			}
		})
	}
	if _, err := s.objectStorage.Save(settings, nil); err != nil {
		t.Fatal(err)
	}
	action := func(version, user int64, confirmation bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"action": "cleanup", "profile_id": "archive", "expected_version": version, "expected_user_id": user, "confirm_cleanup": confirmation})
		return serveAdminCommentAPI(handler, cookie, principal.CSRFToken, http.MethodPost, "https://example.test/api/v1/admin/plugins/object-storage/action", string(body), "", "https://example.test")
	}
	if r := action(2, principal.UserID, false); r.Code != http.StatusBadRequest {
		t.Fatalf("cleanup without confirmation: %d %s", r.Code, r.Body.String())
	}
	if r := action(1, principal.UserID, true); r.Code != http.StatusConflict {
		t.Fatalf("stale cleanup: %d %s", r.Code, r.Body.String())
	}
	if r := action(2, principal.UserID+1, true); r.Code == http.StatusOK {
		t.Fatal("wrong identity accepted")
	}
	if r := action(2, principal.UserID, true); r.Code != http.StatusOK {
		t.Fatalf("confirmed empty cleanup: %d %s", r.Code, r.Body.String())
	}
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/admin/plugins/object-storage/status", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", unauthenticated.Code)
	}
	if err := s.plugins.Uninstall(plugin.ObjectStorage); err != nil {
		t.Fatal(err)
	}
	missing := serveAdminCommentAPI(handler, cookie, "", http.MethodGet, "https://example.test/api/v1/admin/plugins/object-storage/status", "", "", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("uninstalled status=%d", missing.Code)
	}
}

func TestObjectStorageLocalPreviewCannotReadPrivateConfiguration(t *testing.T) {
	s, handler, _, _ := newStorageHTTPTestServer(t)
	if _, err := s.objectStorage.Save(storageHTTPSettings(), nil); err != nil {
		t.Fatal(err)
	}
	previewAuth, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s.auth = previewAuth
	request := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/admin/plugins/object-storage/status", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "storage.example.invalid") {
		t.Fatalf("local private status: %d %s", response.Code, response.Body.String())
	}
	page := serveCommentRequest(handler, nil, http.MethodGet, "http://localhost/admin/plugins/object-storage/", "", "", "127.0.0.1:1234")
	if page.Code != http.StatusOK {
		t.Fatalf("local preview: %d %s", page.Code, page.Body.String())
	}
	if strings.Contains(page.Body.String(), "storage.example.invalid") || strings.Contains(page.Body.String(), "storage-access-id") || strings.Contains(page.Body.String(), "storage-secret-sentinel") {
		t.Fatal("local preview disclosed storage configuration")
	}
}
