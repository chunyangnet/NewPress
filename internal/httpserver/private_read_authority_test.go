package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/feedback"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/navigation"
)

type privateReadAudit struct {
	after func()
}

func (r privateReadAudit) List(context.Context, audit.Query) (audit.Page, error) {
	r.after()
	return audit.Page{Total: 1, Events: []audit.Entry{{ID: 1, CreatedAt: time.Now().UTC(), Action: "comment.edit", ObjectType: "comment", ObjectID: "PRIVATE-EXPORT-MARKER", Actor: "operator"}}}, nil
}

type privateReadFeedback struct {
	feedback.Store
	after func()
}

func (s *privateReadFeedback) Get(ctx context.Context, slug string) (feedback.Board, error) {
	b, err := s.Store.Get(ctx, slug)
	if s.after != nil {
		change := s.after
		s.after = nil
		change()
	}
	return b, err
}

func privateReadFeedbackFixture(t *testing.T, s *Server, owner auth.Principal) feedback.Store {
	t.Helper()
	settings := feedback.Defaults()
	actor := feedbackActor(owner)
	result, err := s.feedback.Apply(t.Context(), "read-exports", actor, feedback.Command{Action: "create", Settings: &settings, Key: "private-read-create"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.feedback.Apply(t.Context(), "read-exports", actor, feedback.Command{Action: "submit", Version: result.Version, Key: "private-read-submit", Title: "Private feedback", Body: "PRIVATE-EXPORT-MARKER", Kind: "issue", Private: true}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return s.feedback
}

func TestPrivateExportsRecheckOriginalSessionAfterStorage(t *testing.T) {
	for _, operation := range []string{"audit-api", "audit-export", "navigation", "support", "circle", "feedback"} {
		for _, change := range []string{"logout", "logout-with-fallback", "permission", "suspension", "allowed"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
				user, cookie, _ := createAndLoginTestUser(t, manager, owner, "read-actor", "read@example.test", "Read actor", "administrator")
				var fallback *http.Cookie
				if change == "logout-with-fallback" {
					// The same user's second session must not take over either.
					token, _, err := manager.AuthenticateContext(t.Context(), "read-second-session", user.Username, "Account password for read-actor 2026")
					if err != nil {
						t.Fatal(err)
					}
					fallback = &http.Cookie{Name: auth.CookieName, Value: token}
				}
				changed := false
				after := func() {
					if changed {
						return
					}
					changed = true
					switch change {
					case "logout", "logout-with-fallback":
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Fatal(err)
						}
					case "permission", "suspension":
						role, status := "author", user.Status
						if change == "suspension" {
							role, status = "administrator", auth.UserStatusSuspended
						}
						if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: status, RoleSlugs: []string{role}}); err != nil {
							t.Fatal(err)
						}
					}
				}
				var path string
				switch operation {
				case "audit-api", "audit-export":
					s.SetAuditReader(privateReadAudit{after: after})
					path = "/api/v1/admin/audit-events"
					if operation == "audit-export" {
						path = "/admin/tools/audit-events.json"
					}
				case "navigation", "support":
					if _, err := s.navigation.Create(t.Context(), navigation.MenuInput{Name: "PRIVATE-EXPORT-MARKER", Slug: "private-export"}, owner.Username); err != nil {
						t.Fatal(err)
					}
					s.navigation = &navigationAuthorityStore{Store: s.navigation, afterSnapshot: after}
					path = "/admin/tools/navigation-export.json"
					if operation == "support" {
						path = "/admin/tools/support-report.json"
					}
				case "circle":
					store := attachmentAuthorityCircle(t, s, owner)
					attachmentAuthorityApply(t, store, community.Actor{ID: owner.UserID, Admin: true}, community.Command{Action: "topic", Kind: "discussion", Title: "Private export", Body: "PRIVATE-EXPORT-MARKER"})
					s.SetCommunityStore(&circleAttachmentSnapshotStore{Store: store, afterGet: func(int) { after() }})
					path = "/api/v1/community/circles/authority/export"
				case "feedback":
					store := privateReadFeedbackFixture(t, s, owner)
					s.SetFeedbackStore(&privateReadFeedback{Store: store, after: after})
					path = "/api/v1/feedback/read-exports/export"
				}
				request := httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil)
				request.AddCookie(cookie)
				if fallback != nil {
					request.AddCookie(fallback)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if !changed {
					t.Fatalf("storage boundary not reached: %d %s", response.Code, response.Body.String())
				}
				if change == "allowed" {
					if response.Code != http.StatusOK || operation != "support" && !strings.Contains(response.Body.String(), "PRIVATE-EXPORT-MARKER") {
						t.Fatalf("authorized read failed: %d %s", response.Code, response.Body.String())
					}
					return
				}
				want := http.StatusForbidden
				if change != "permission" {
					want = http.StatusUnauthorized
					if strings.HasPrefix(path, "/admin/") {
						want = http.StatusSeeOther
					}
				}
				if response.Code != want || strings.Contains(response.Body.String(), "PRIVATE-EXPORT-MARKER") || response.Header().Get("Content-Disposition") != "" {
					t.Fatalf("revoked read returned data: status=%d want=%d body=%s", response.Code, want, response.Body.String())
				}
			})
		}
	}
}

func TestMediaPreviewRechecksAuthorityDuringStream(t *testing.T) {
	for _, change := range []string{"logout", "logout-with-fallback", "permission", "suspension", "cancel", "allowed"} {
		t.Run(change, func(t *testing.T) {
			s, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
			user, cookie, _ := createAndLoginTestUser(t, manager, owner, "preview-actor", "preview@example.test", "Preview actor", "administrator")
			const tail = "PRIVATE-RETAINED-MEDIA-TAIL"
			data := "%PDF-1.7\n" + strings.Repeat("x", 2<<20) + tail + "\n%%EOF"
			item, err := s.media.Upload(t.Context(), strings.NewReader(data), media.UploadInput{Filename: "private.pdf"}, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.media.Trash(item.ID, item.Version); err != nil {
				t.Fatal(err)
			}
			if _, _, err = s.media.Open(item.ID); !errors.Is(err, media.ErrNotFound) {
				t.Fatal("retained media fixture must be private", err)
			}
			var changed atomic.Bool
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				r = r.WithContext(ctx)
				handler.ServeHTTP(&storageAfterWrite{ResponseWriter: w, after: func() {
					switch change {
					case "cancel":
						cancel()
					case "logout", "logout-with-fallback":
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Error(err)
						}
					case "permission", "suspension":
						role, status := "subscriber", user.Status
						if change == "suspension" {
							role, status = "administrator", auth.UserStatusSuspended
						}
						if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: status, RoleSlugs: []string{role}}); err != nil {
							t.Error(err)
						}
					}
					changed.Store(true)
				}}, r)
			}))
			defer local.Close()
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("%s/admin/media/%d/preview/", local.URL, item.ID), nil)
			if err != nil {
				t.Fatal(err)
			}
			request.AddCookie(cookie)
			if change == "logout-with-fallback" {
				request.AddCookie(ownerCookie)
			}
			response, err := local.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			got, readErr := io.ReadAll(response.Body)
			if !changed.Load() || response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
				t.Fatalf("preview boundary not reached: status=%d bytes=%d error=%v", response.StatusCode, len(got), readErr)
			}
			if change == "allowed" {
				if readErr != nil || string(got) != data {
					t.Fatal("authorized media preview lost bytes", len(got), readErr)
				}
				return
			}
			if !errors.Is(readErr, io.ErrUnexpectedEOF) || len(got) == 0 || len(got) > 1<<20 || strings.Contains(string(got), tail) || strings.Contains(string(got), "internal_server_error") {
				t.Fatalf("revoked preview continued: bytes=%d error=%v", len(got), readErr)
			}
		})
	}
}

func TestPrivateExportsRecheckResourcePermissions(t *testing.T) {
	for _, operation := range []string{"circle", "feedback"} {
		t.Run(operation, func(t *testing.T) {
			s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
			_, cookie, actor := createAndLoginTestUser(t, manager, owner, "export-moderator", "moderator@example.test", "Export moderator", "editor")
			changed := false
			path := "/api/v1/feedback/read-exports/export"
			if operation == "circle" {
				store := attachmentAuthorityCircle(t, s, actor)
				attachmentAuthorityApply(t, store, community.Actor{ID: owner.UserID}, community.Command{Action: "join"})
				attachmentAuthorityApply(t, store, community.Actor{ID: actor.UserID}, community.Command{Action: "topic", Kind: "discussion", Title: "Private circle", Body: "PRIVATE-EXPORT-MARKER"})
				s.SetCommunityStore(&circleAttachmentSnapshotStore{Store: store, afterGet: func(int) {
					if !changed {
						changed = true
						attachmentAuthorityApply(t, store, community.Actor{ID: actor.UserID}, community.Command{Action: "transfer", UserID: owner.UserID})
					}
				}})
				path = "/api/v1/community/circles/authority/export"
			} else {
				store := privateReadFeedbackFixture(t, s, owner)
				s.SetFeedbackStore(&privateReadFeedback{Store: store, after: func() {
					changed = true
					current, err := store.Get(t.Context(), "read-exports")
					if err != nil {
						t.Fatal(err)
					}
					settings := current.Settings
					for i := range settings.Permissions {
						settings.Permissions[i].Moderate = false
					}
					if _, err = store.Apply(t.Context(), current.Slug, feedbackActor(owner), feedback.Command{Action: "configure", Key: "export-revoke-moderation", Version: current.Version, Settings: &settings}, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}})
			}
			response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, cookie, nil)
			if !changed || response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "PRIVATE-EXPORT-MARKER") || response.Header().Get("Content-Disposition") != "" {
				t.Fatalf("resource revocation did not block export: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

type privateReadSessionStore struct {
	auth.SessionStore
	lookups atomic.Int64
	failAt  int64
}

func (s *privateReadSessionStore) Get(ctx context.Context, id string) (auth.SessionRecord, error) {
	if n := s.lookups.Add(1); s.failAt > 0 && n >= s.failAt {
		return auth.SessionRecord{}, errors.New("PRIVATE-SESSION-ERROR-DETAILS")
	}
	return s.SessionStore.Get(ctx, id)
}

func TestPrivateReadsSessionOutagePreservesCookies(t *testing.T) {
	for _, operation := range []string{"audit", "circle", "feedback", "media"} {
		t.Run(operation, func(t *testing.T) {
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			users, err := auth.NewFileUserStore("")
			if err != nil {
				t.Fatal(err)
			}
			sessions := &privateReadSessionStore{SessionStore: auth.NewMemorySessionStore()}
			const password = "Private read outage password 2026"
			hash, err := auth.HashPassword(password)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: users, Sessions: sessions, BootstrapEmail: "owner@example.test"})
			if err != nil {
				t.Fatal(err)
			}
			token, owner, err := manager.AuthenticateContext(t.Context(), "private-read-outage", "owner", password)
			if err != nil {
				t.Fatal(err)
			}
			s.auth = manager
			var path string
			switch operation {
			case "audit":
				s.SetAuditReader(privateReadAudit{after: func() {}})
				path = "/admin/tools/audit-events.json"
			case "circle":
				attachmentAuthorityCircle(t, s, owner)
				path = "/api/v1/community/circles/authority/export"
			case "feedback":
				privateReadFeedbackFixture(t, s, owner)
				path = "/api/v1/feedback/read-exports/export"
			case "media":
				item, err := s.media.Upload(t.Context(), strings.NewReader("%PDF-1.7\nPRIVATE-EXPORT-MARKER\n%%EOF"), media.UploadInput{Filename: "private.pdf"}, 8192)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.media.Trash(item.ID, item.Version); err != nil {
					t.Fatal(err)
				}
				path = fmt.Sprintf("/admin/media/%d/preview/", item.ID)
			}
			sessions.lookups.Store(0)
			sessions.failAt = 2 // Entry succeeds; the post-storage recheck fails.
			response := serveCookieRequest(s.routes(), http.MethodGet, "https://example.test"+path, &http.Cookie{Name: auth.CookieName, Value: token}, nil)
			if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "5" || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Content-Disposition") != "" || response.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("outage did not preserve the browser session: %d %v", response.Code, response.Header())
			}
			if strings.Contains(response.Body.String(), "PRIVATE-") {
				t.Fatal("outage disclosed private data or storage errors")
			}
		})
	}
}

func TestFreshSessionRefreshPreservesOriginalToken(t *testing.T) {
	s, _, manager, _, original, owner := newPersistentAdminTestServer(t)
	token, _, err := manager.AuthenticateContext(t.Context(), "fallback-same-user", owner.Username, "Persistent owner password 2026")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://example.test/api/v1/admin/session", nil)
	request.AddCookie(original)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	response := httptest.NewRecorder()
	s.sessionState(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, selected, ok := s.currentAdminSession(r)
		if !ok || selected != original.Value {
			t.Fatal("wrong initial session")
		}
		if err := manager.RevokeSessionContext(t.Context(), original.Value); err != nil {
			t.Fatal(err)
		}
		fresh, ok := s.freshSessionRequest(w, r)
		if !ok {
			t.Fatal("revocation is not a session-store outage")
		}
		if p, token, ok := s.currentAdminSession(fresh); ok {
			t.Fatalf("another session took over the revoked request: user=%d token_changed=%v", p.UserID, token != original.Value)
		}
	})).ServeHTTP(response, request)
}

func TestFeedbackExportRevocationAbortsBeforeCSVFlush(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	privateReadFeedbackFixture(t, s, owner)
	var revoked atomic.Bool
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(&storageAfterWrite{ResponseWriter: w, after: func() {
			// Expose the UTF-8 BOM as a real partial HTTP response before
			// revocation, while the sensitive CSV rows are still buffered.
			w.(http.Flusher).Flush()
			if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
				t.Error(err)
			}
			revoked.Store(true)
		}}, r)
	}))
	defer local.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, local.URL+"/api/v1/feedback/read-exports/export", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	response, err := local.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	got, readErr := io.ReadAll(response.Body)
	if !revoked.Load() || response.StatusCode != http.StatusOK || !errors.Is(readErr, io.ErrUnexpectedEOF) || string(got) != "\xef\xbb\xbf" {
		t.Fatalf("revoked CSV export disclosed buffered data: status=%d bytes=%q error=%v", response.StatusCode, got, readErr)
	}
}
