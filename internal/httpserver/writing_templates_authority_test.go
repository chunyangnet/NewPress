package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/writingtemplate"
)

const templateAuthorityMarker = "PRIVATE-TEMPLATE-AUTHORITY"

type templateAuthorityStorage struct {
	writingtemplate.Storage
	afterRead func()
	reads     int
	writes    int
}

func (s *templateAuthorityStorage) Read(ctx context.Context) ([]byte, error) {
	s.reads++
	raw, err := s.Storage.Read(ctx)
	if s.afterRead != nil {
		after := s.afterRead
		s.afterRead = nil
		after()
	}
	return raw, err
}

func (s *templateAuthorityStorage) Write(ctx context.Context, expected [sha256.Size]byte, raw []byte) error {
	s.writes++
	return s.Storage.Write(ctx, expected, raw)
}

func templateAuthorityFixture(t *testing.T, s *Server) (*templateAuthorityStorage, writingtemplate.Result, []byte) {
	t.Helper()
	store := &templateAuthorityStorage{Storage: &writingtemplate.MemoryStorage{}}
	service, err := writingtemplate.New(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Apply(t.Context(), writingtemplate.Command{
		Action: "save", RequestKey: strings.Repeat("a", 64), Actor: "user:1",
		Definition: &writingtemplate.Definition{BasePrompt: "article", Title: templateAuthorityMarker, Instruction: "Keep the original facts."},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := service.Export(t.Context(), created.ID, created.Version)
	if err != nil {
		t.Fatal(err)
	}
	s.SetWritingTemplates(service)
	store.reads, store.writes = 0, 0
	return store, created, raw
}

func templateAuthorityPayload(t *testing.T, action string, item writingtemplate.Result, raw []byte) string {
	t.Helper()
	var payload any
	switch action {
	case "save", "replay":
		payload = map[string]any{"action": "save", "definition": writingtemplate.Definition{BasePrompt: "article", Title: templateAuthorityMarker, Instruction: "Keep the original facts."}}
	case "archive":
		payload = map[string]any{"action": "archive", "id": item.ID, "expected_version": item.Version}
	case "import":
		preview, err := writingtemplate.PreviewImport(raw)
		if err != nil {
			t.Fatal(err)
		}
		payload = map[string]any{"action": "import", "document": string(raw), "import_sha256": preview.SHA256, "confirm_import": true}
	case "preview":
		payload = map[string]string{"document": string(raw)}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func templateAuthorityRequest(cookie, fallback *http.Cookie, actor auth.Principal, method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body))
	r.AddCookie(cookie)
	if fallback != nil {
		r.AddCookie(fallback)
	}
	for key, value := range authenticatedAPIHeaders(actor) {
		r.Header.Set(key, value)
	}
	r.Header.Set("Idempotency-Key", "template-authority-request")
	return r
}

func templateAuthorityChange(t *testing.T, manager *auth.Manager, owner auth.Principal, user auth.User, cookie *http.Cookie, change string) (func(), *http.Cookie) {
	t.Helper()
	var fallback *http.Cookie
	if change == "logout-with-fallback" {
		token, _, err := manager.AuthenticateContext(t.Context(), "template-second-session", user.Username, "Account password for template-session 2026")
		if err != nil {
			t.Fatal(err)
		}
		fallback = &http.Cookie{Name: auth.CookieName, Value: token}
	}
	return func() {
		switch change {
		case "logout", "logout-with-fallback":
			if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
				t.Fatal(err)
			}
		case "permission", "suspension":
			role, status := "editor", user.Status
			if change == "suspension" {
				role, status = "administrator", auth.UserStatusSuspended
			}
			if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: status, RoleSlugs: []string{role}}); err != nil {
				t.Fatal(err)
			}
		}
	}, fallback
}

func templateAuthorityAssertDenied(t *testing.T, response *httptest.ResponseRecorder, change string) {
	t.Helper()
	want := http.StatusUnauthorized
	if change == "permission" {
		want = http.StatusForbidden
	}
	if response.Code != want || strings.Contains(response.Body.String(), templateAuthorityMarker) || response.Header().Get("Content-Disposition") != "" || response.Header().Get("Idempotency-Replayed") != "" {
		t.Errorf("revoked request returned data or success: status=%d want=%d headers=%v body=%s", response.Code, want, response.Header(), response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
		t.Error("private response is cacheable")
	}
}

func TestWritingTemplateRechecksAuthorityAfterBody(t *testing.T) {
	for _, operation := range []string{"save", "import", "preview"} {
		for _, change := range []string{"logout", "logout-with-fallback", "permission", "suspension", "allowed"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "template-session", "template-session@example.test", "Template actor", "administrator")
				store, item, raw := templateAuthorityFixture(t, s)
				before, err := store.Storage.Read(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				after, fallback := templateAuthorityChange(t, manager, owner, user, cookie, change)
				path := "/api/v1/admin/ai/templates"
				if operation == "preview" {
					path = "/api/v1/admin/ai/template-import-preview"
				}
				r := templateAuthorityRequest(cookie, fallback, actor, http.MethodPost, path, templateAuthorityPayload(t, operation, item, raw))
				changed := false
				r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: func() { changed = true; after() }}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				if !changed {
					t.Fatalf("body boundary not reached: %d %s", response.Code, response.Body.String())
				}
				if change == "allowed" {
					if response.Code != http.StatusOK || operation != "preview" && store.writes != 1 {
						t.Fatalf("authorized request failed: %d %s, writes=%d", response.Code, response.Body.String(), store.writes)
					}
					return
				}
				templateAuthorityAssertDenied(t, response, change)
				current, err := store.Storage.Read(t.Context())
				if err != nil || !bytes.Equal(current, before) || store.writes != 0 || store.reads != 0 {
					t.Errorf("revoked body reached template storage: reads=%d writes=%d changed=%v err=%v", store.reads, store.writes, !bytes.Equal(current, before), err)
				}
			})
		}
	}
}

func TestWritingTemplateRechecksAuthorityAfterStorage(t *testing.T) {
	for _, operation := range []string{"list", "detail", "export", "save", "archive", "import", "replay"} {
		for _, change := range []string{"logout", "logout-with-fallback", "permission", "suspension", "allowed"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "template-session", "template-session@example.test", "Template actor", "administrator")
				store, item, raw := templateAuthorityFixture(t, s)
				method, path, body := http.MethodGet, "/api/v1/admin/ai/templates", ""
				switch operation {
				case "detail":
					path += "/" + item.ID
				case "export":
					path += "/" + item.ID + "/export?version=1"
				case "save", "archive", "import", "replay":
					method = http.MethodPost
					body = templateAuthorityPayload(t, operation, item, raw)
				}
				if operation == "replay" {
					first := httptest.NewRecorder()
					handler.ServeHTTP(first, templateAuthorityRequest(cookie, nil, actor, method, path, body))
					if first.Code != http.StatusOK {
						t.Fatal(first.Code, first.Body.String())
					}
				}
				before, err := store.Storage.Read(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				store.reads, store.writes = 0, 0
				after, fallback := templateAuthorityChange(t, manager, owner, user, cookie, change)
				changed := false
				store.afterRead = func() { changed = true; after() }
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, templateAuthorityRequest(cookie, fallback, actor, method, path, body))
				if !changed {
					t.Fatalf("storage boundary not reached: %d %s", response.Code, response.Body.String())
				}
				if change == "allowed" {
					if response.Code != http.StatusOK {
						t.Fatal(response.Code, response.Body.String())
					}
					if method == http.MethodGet && !strings.Contains(response.Body.String(), templateAuthorityMarker) {
						t.Fatal("authorized template content missing")
					}
					if operation == "replay" && (store.writes != 0 || response.Header().Get("Idempotency-Replayed") != "true") {
						t.Fatal("authorized replay changed state")
					}
					return
				}
				templateAuthorityAssertDenied(t, response, change)
				current, err := store.Storage.Read(t.Context())
				if err != nil || !bytes.Equal(current, before) || store.writes != 0 {
					t.Errorf("revoked request changed templates or receipts: writes=%d err=%v", store.writes, err)
				}
			})
		}
	}
}

func TestWritingTemplateAuthoritySessionOutagePreservesCookies(t *testing.T) {
	for _, boundary := range []string{"body", "storage", "export"} {
		t.Run(boundary, func(t *testing.T) {
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			users, err := auth.NewFileUserStore("")
			if err != nil {
				t.Fatal(err)
			}
			sessions := &privateReadSessionStore{SessionStore: auth.NewMemorySessionStore()}
			const password = "Template outage password 2026"
			hash, err := auth.HashPassword(password)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: users, Sessions: sessions, BootstrapEmail: "owner@example.test"})
			if err != nil {
				t.Fatal(err)
			}
			token, actor, err := manager.AuthenticateContext(t.Context(), "template-outage", "owner", password)
			if err != nil {
				t.Fatal(err)
			}
			s.auth = manager
			store, item, raw := templateAuthorityFixture(t, s)
			before, err := store.Storage.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			fail := func() { sessions.failAt = sessions.lookups.Load() + 1 }
			method, path := http.MethodPost, "/api/v1/admin/ai/templates"
			if boundary == "export" {
				method, path = http.MethodGet, fmt.Sprintf("%s/%s/export?version=1", path, item.ID)
			}
			r := templateAuthorityRequest(&http.Cookie{Name: auth.CookieName, Value: token}, nil, actor, method, path, templateAuthorityPayload(t, "save", item, raw))
			if boundary == "body" {
				r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: fail}
			} else {
				store.afterRead = fail
			}
			response := httptest.NewRecorder()
			s.routes().ServeHTTP(response, r)
			if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "5" || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Content-Disposition") != "" || response.Header().Get("WWW-Authenticate") != "" {
				t.Errorf("outage lost browser session: %d %v", response.Code, response.Header())
			}
			current, err := store.Storage.Read(t.Context())
			if err != nil || !bytes.Equal(current, before) || store.writes != 0 || strings.Contains(response.Body.String(), "PRIVATE-") {
				t.Errorf("outage changed storage or leaked details: writes=%d err=%v body=%s", store.writes, err, response.Body.String())
			}
		})
	}
}

func TestWritingTemplateHistoryRevocationDuringResponse(t *testing.T) {
	for _, change := range []string{"logout", "permission", "cancel", "allowed"} {
		t.Run(change, func(t *testing.T) {
			s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
			user, cookie, _ := createAndLoginTestUser(t, manager, owner, "template-session", "template-session@example.test", "Template actor", "administrator")
			store, item, _ := templateAuthorityFixture(t, s)
			before, err := store.Storage.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Schema    int                        `json:"schema"`
				Kind      string                     `json:"kind"`
				Templates []writingtemplate.Template `json:"templates"`
				Receipts  json.RawMessage            `json:"receipts"`
			}
			if err = json.Unmarshal(before, &doc); err != nil {
				t.Fatal(err)
			}
			const tail = "PRIVATE-TEMPLATE-HISTORY-TAIL"
			// Construct a valid large history without repeatedly rewriting it.
			// This is a response fixture, not evidence of 80 successful API writes.
			for version := int64(2); version <= 80; version++ {
				revision := doc.Templates[0].Revisions[0]
				revision.Version = version
				revision.Definition.Instruction = strings.Repeat("x", 12000)
				revision.Definition.Example = strings.Repeat("y", 8000)
				if version == 80 {
					revision.Definition.Example = tail
				}
				doc.Templates[0].Revisions = append(doc.Templates[0].Revisions, revision)
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Storage.Write(t.Context(), sha256.Sum256(before), raw); err != nil {
				t.Fatal(err)
			}
			var changed atomic.Bool
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				handler.ServeHTTP(&storageAfterWrite{ResponseWriter: w, after: func() {
					switch change {
					case "logout":
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Error(err)
						}
					case "permission":
						if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: user.Status, RoleSlugs: []string{"editor"}}); err != nil {
							t.Error(err)
						}
					case "cancel":
						cancel()
					}
					changed.Store(true)
				}}, r.WithContext(ctx))
			}))
			defer local.Close()
			r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, local.URL+"/api/v1/admin/ai/templates/"+item.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.AddCookie(cookie)
			response, err := local.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			got, readErr := io.ReadAll(response.Body)
			if !changed.Load() || response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
				t.Fatalf("response boundary not reached: %d bytes=%d err=%v", response.StatusCode, len(got), readErr)
			}
			if change == "allowed" {
				var history writingtemplate.Template
				if readErr != nil || json.Unmarshal(got, &history) != nil || len(history.Revisions) != 80 || history.Revisions[79].Definition.Example != tail {
					t.Fatal("authorized history was incomplete", len(got), readErr)
				}
				return
			}
			if !errors.Is(readErr, io.ErrUnexpectedEOF) || len(got) == 0 || len(got) > 1<<20 || bytes.Contains(got, []byte(tail)) || bytes.Contains(got, []byte("internal_server_error")) {
				t.Fatalf("revoked history continued: bytes=%d err=%v", len(got), readErr)
			}
		})
	}
}
