package httpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/plugin"
)

func TestObjectStorageMediaDeliveryAndLocalFallback(t *testing.T) {
	s, handler, _, _ := newStorageHTTPTestServer(t)
	var mu sync.Mutex
	objects := map[string][]byte{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			objects[r.URL.Path] = data
			w.WriteHeader(http.StatusOK)
			return
		}
		data, ok := objects[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	}))
	defer remote.Close()
	c := storageHTTPSettings()
	c.Mode = "redirect"
	c.Profiles[0].Endpoint = remote.URL
	c.Profiles[0].AllowPrivate, c.Profiles[0].AllowHTTP = true, true
	c.Profiles[0].PublicURL = "https://cdn.example.test/media"
	if _, err := s.objectStorage.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	item, err := s.media.Upload(t.Context(), strings.NewReader("%PDF-1.7\nStorage delivery\n%%EOF"), media.UploadInput{Filename: "文档.pdf"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "https://example.test"+path, nil))
		return response
	}
	if r := request(http.MethodGet, item.PublicURL); r.Code != http.StatusOK {
		t.Fatalf("unsynced local fallback: %d", r.Code)
	}
	if err = s.plugins.SetEnabled(plugin.ObjectStorage, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.objectStorage.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := s.objectStorage.Status()
		if err != nil {
			t.Fatal(err)
		}
		if status.Counts["synced"] == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync timeout: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	r := request(http.MethodGet, item.PublicURL)
	if r.Code != http.StatusTemporaryRedirect || !strings.HasPrefix(r.Header().Get("Location"), "https://cdn.example.test/media/media/newpress-") || r.Header().Get("Cache-Control") != "private, no-store" || r.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("delivery: %d %v", r.Code, r.Header())
	}
	if r = request(http.MethodHead, item.PublicURL); r.Code != http.StatusOK || r.Header().Get("Location") != "" {
		t.Fatalf("HEAD must keep local semantics: %d %v", r.Code, r.Header())
	}
	if r = request(http.MethodGet, "/"); r.Code != http.StatusOK || !strings.Contains(r.Header().Get("Content-Security-Policy"), "img-src 'self' data: https://cdn.example.test") {
		t.Fatalf("delivery CSP: %d %v", r.Code, r.Header())
	}
	if err = s.plugins.SetEnabled(plugin.ObjectStorage, false); err != nil {
		t.Fatal(err)
	}
	if r = request(http.MethodGet, item.PublicURL); r.Code != http.StatusOK || r.Header().Get("Location") != "" {
		t.Fatalf("disabled local fallback: %d %v", r.Code, r.Header())
	}
	if r = request(http.MethodGet, "/"); strings.Contains(r.Header().Get("Content-Security-Policy"), "cdn.example.test") {
		t.Fatal("disabled plugin retains CSP origin")
	}
	if _, err = s.media.Trash(item.ID, item.Version); err != nil {
		t.Fatal(err)
	}
	if r = request(http.MethodGet, item.PublicURL); r.Code != http.StatusNotFound {
		t.Fatalf("trashed media delivery: %d", r.Code)
	}
}

// Optional static fixtures exercise the real templates/assets in a browser
// without starting an authenticated server or touching a running site.
func TestObjectStorageBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_STORAGE_UI_FIXTURE")
	if directory == "" {
		t.Skip("set NEWPRESS_STORAGE_UI_FIXTURE for static browser fixtures")
	}
	s, handler, cookie, _ := newStorageHTTPTestServer(t)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, style := range []string{"classic", "liquid-glass"} {
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) != 0 {
			t.Fatalf("style: %v %v", fields, err)
		}
		r := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/plugins/object-storage/", "", "", "8.8.8.8:80")
		if r.Code != http.StatusOK {
			t.Fatalf("fixture: %d", r.Code)
		}
		if err := os.WriteFile(filepath.Join(directory, style+".html"), r.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		css := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/theme-settings.css", "", "", "8.8.8.8:80")
		if css.Code != http.StatusOK {
			t.Fatalf("fixture CSS: %d", css.Code)
		}
		if err := os.WriteFile(filepath.Join(directory, style+".css"), css.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
