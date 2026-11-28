package httpserver

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
)

type mediaAuthorityBody struct {
	io.ReadCloser
	onRead func()
}

func (r *mediaAuthorityBody) Read(p []byte) (int, error) {
	if r.onRead != nil {
		change := r.onRead
		r.onRead = nil
		change()
	}
	return r.ReadCloser.Read(p)
}

func TestMediaUploadRechecksAuthorityAfterBody(t *testing.T) {
	for _, route := range []string{"api", "form"} {
		for _, change := range []string{"permission", "logout", "allowed"} {
			t.Run(route+"/"+change, func(t *testing.T) {
				s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "upload-session", "upload-session@example.test", "Upload editor", "editor")
				// Spill the multipart payload to disk as a large real upload does.
				payload := append(httpTestPNG(t, 4, 3), bytes.Repeat([]byte{0}, 600<<10)...)
				var request *http.Request
				if route == "api" {
					request = mediaAPIUploadRequest(t, cookie.Value, actor.CSRFToken, "upload.png", payload)
				} else {
					request = mediaUploadRequest(t, cookie.Value, actor.CSRFToken, "upload.png", payload, "Upload fixture", "", "")
				}
				request.Body = &mediaAuthorityBody{ReadCloser: request.Body, onRead: func() {
					switch change {
					case "permission":
						if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: user.Status, RoleSlugs: []string{"author"}}); err != nil {
							t.Fatal(err)
						}
					case "logout":
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Fatal(err)
						}
					}
				}}
				before := s.media.Snapshot().Count
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if change == "allowed" {
					want := http.StatusCreated
					if route == "form" {
						want = http.StatusSeeOther
					}
					if response.Code != want || s.media.Snapshot().Count != before+1 {
						t.Fatalf("authorized upload failed: %d %s", response.Code, response.Body.String())
					}
					return
				}
				want := http.StatusForbidden
				if change == "logout" {
					want = http.StatusUnauthorized
					if route == "form" {
						want = http.StatusSeeOther
						if !strings.HasPrefix(response.Header().Get("Location"), "/admin/login/") {
							t.Error("logged-out form did not redirect to login")
						}
					}
				}
				if response.Code != want || s.media.Snapshot().Count != before {
					t.Fatalf("revoked upload wrote media: status=%d delta=%d", response.Code, s.media.Snapshot().Count-before)
				}
			})
		}
	}
}
