package httpserver

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMediaAPIUploadSourceValidationAndPersistence(t *testing.T) {
	s, handler, token, principal, _ := newMediaHTTPTestServer(t, 4096)
	for _, source := range []string{"editor-cover", "series-cover", "settings-image", "link-logo", "avatar", "unknown"} {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for key, value := range map[string]string{"csrf_token": principal.CSRFToken, "title": "", "alt_text": "", "caption": "", "upload_source": source} {
			if err := form.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
		part, err := form.CreateFormFile("file", "source.png")
		if err != nil {
			t.Fatal(err)
		}
		part.Write(httpTestPNG(t, 3, 4))
		form.Close()
		r := authenticatedMediaRequest(http.MethodPost, "https://example.test/api/v1/admin/media", &body, token)
		r.Header.Set("Content-Type", form.FormDataContentType())
		r.Header.Set("X-CSRF-Token", principal.CSRFToken)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := http.StatusCreated
		if source == "unknown" {
			want = http.StatusBadRequest
		}
		if w.Code != want {
			t.Fatalf("%s: %d %s", source, w.Code, w.Body.String())
		}
	}
	for id, source := range map[int64]string{1: "editor-cover", 2: "series-cover", 3: "settings-image", 4: "link-logo", 5: "avatar"} {
		item, err := s.media.Get(id)
		if err != nil || item.UploadSource != source {
			t.Fatal(item, err)
		}
	}
	if s.media.Snapshot().Count != 5 {
		t.Fatal("rejected source persisted")
	}
}
