package httpserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
)

type aiGenerationFaultStore struct {
	auth.SessionStore
	fail atomic.Bool
}

func (s *aiGenerationFaultStore) Get(ctx context.Context, id string) (auth.SessionRecord, error) {
	if s.fail.Load() {
		return auth.SessionRecord{}, errors.New("PRIVATE-SESSION-DETAIL")
	}
	return s.SessionStore.Get(ctx, id)
}

type aiGenerationBoundaryWriter struct {
	*httptest.ResponseRecorder
	deadline   func()
	afterWrite func([]byte)
	write      func([]byte) (int, error)
	flush      func() error
}

func (w *aiGenerationBoundaryWriter) SetWriteDeadline(time.Time) error {
	runAutosaveBoundary(&w.deadline)
	return nil
}

func (w *aiGenerationBoundaryWriter) Write(p []byte) (int, error) {
	if w.write != nil {
		return w.write(p)
	}
	n, err := w.ResponseRecorder.Write(p)
	if w.afterWrite != nil {
		w.afterWrite(p)
	}
	return n, err
}

func (w *aiGenerationBoundaryWriter) FlushError() error {
	w.ResponseRecorder.Flush()
	if w.flush != nil {
		return w.flush()
	}
	return nil
}

func serveAIGenerationBoundary(h http.Handler, w http.ResponseWriter, r *http.Request) (aborted bool) {
	defer func() {
		if v := recover(); v == http.ErrAbortHandler {
			aborted = true
		} else if v != nil {
			panic(v)
		}
	}()
	h.ServeHTTP(w, r)
	return
}

func TestAIGenerationAuthorityReady(t *testing.T) {
	for _, boundary := range []string{"before-ready", "after-ready"} {
		for _, change := range []string{"logout", "module-restarted", "outage", "cancel", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				manager, _, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, nil)
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				changed := false
				after := func() {
					changed = true
					switch change {
					case "logout":
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Error(err)
						}
					case "module-restarted":
						accountAuthorityModule(t, s, "ai", false)
						accountAuthorityModule(t, s, "ai", true)
					case "outage":
						fault.fail.Store(true)
					case "cancel":
						cancel()
					}
				}
				var calls atomic.Int32
				setAIResponse(s, 200, "text/event-stream", aiFrame(aiGenerationAuthorityText, "stop")+"data: [DONE]\n\n", func(*http.Request) { calls.Add(1) })
				w := &aiGenerationBoundaryWriter{ResponseRecorder: httptest.NewRecorder()}
				if boundary == "before-ready" {
					w.deadline = after
				} else {
					w.flush = func() error { runAutosaveBoundary(&after); return nil }
				}
				aborted := serveAIGenerationBoundary(s.routes(), w, aiGenerationAuthorityRequest(cookie, actor, ctx, aiGenerationJSON()))
				waitAIGenerationSlot(t, s, actor.UserID)
				wantCalls := int32(0)
				if change == "allowed" {
					wantCalls = 1
				}
				if !changed || calls.Load() != wantCalls || strings.Contains(w.Body.String(), "PRIVATE-") != (change == "allowed") {
					t.Fatal("ready authority boundary", changed, calls.Load(), w.Code, w.Body.String())
				}
				if change == "cancel" {
					if boundary == "before-ready" && (!aborted || w.Body.Len() != 0) || strings.Contains(w.Body.String(), "event: error") {
						t.Fatal("cancelled request produced output", aborted, w.Body.String())
					}
					return
				}
				want := 200
				if boundary == "before-ready" {
					switch change {
					case "logout":
						want = 401
					case "module-restarted":
						want = 409
					case "outage":
						want = 503
					}
				}
				if aborted || w.Code != want || want != 200 && !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
					t.Fatal("ready failure status", aborted, w.Code, want, w.Header())
				}
				if change == "outage" && (w.Header().Get("Set-Cookie") != "" || w.Header().Get("WWW-Authenticate") != "" || want == 503 && w.Header().Get("Retry-After") != "5") {
					t.Fatal("outage discarded session or retry", w.Header())
				}
				if boundary == "after-ready" && change != "allowed" && !strings.Contains(w.Body.String(), "event: error") {
					t.Fatal("SSE terminal error missing", w.Body.String())
				}
			})
		}
	}
}

func TestAIGenerationAuthoritySessionOutage(t *testing.T) {
	for _, boundary := range []string{"body", "categories", "upstream", "completion"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
			_, _, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, nil)
			fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
			sessions.SessionStore = fault
			after := func() { fault.fail.Store(true) }
			if boundary == "categories" {
				s.repository = &editorAuthorityRepository{Repository: repository, afterTerms: after}
			}
			body := &aiGenerationTrackedBody{ReadCloser: io.NopCloser(strings.NewReader(aiFrame(aiGenerationAuthorityText, "stop") + "data: [DONE]\n\n"))}
			if boundary == "completion" {
				body.afterClose = after
			}
			var calls atomic.Int32
			s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				if boundary == "upstream" {
					after()
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
			})}}
			r := aiGenerationAuthorityRequest(cookie, actor, t.Context(), aiGenerationJSON())
			if boundary == "body" {
				r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
			}
			w, aborted := commentAuthorityServe(s.routes(), r)
			waitAIGenerationSlot(t, s, actor.UserID)
			want, wantCalls := 503, int32(0)
			if boundary == "upstream" || boundary == "completion" {
				want, wantCalls = 200, 1
				if !body.closed.Load() || !strings.Contains(w.Body.String(), "event: error") {
					t.Fatal("outage did not stop stream", w.Body.String())
				}
			}
			if !fault.fail.Load() || aborted || w.Code != want || calls.Load() != wantCalls || w.Header().Get("Set-Cookie") != "" || w.Header().Get("WWW-Authenticate") != "" || strings.Contains(w.Body.String(), "PRIVATE-SESSION-DETAIL") || strings.Contains(w.Body.String(), "event: done") || boundary != "completion" && strings.Contains(w.Body.String(), aiGenerationAuthorityText) {
				t.Fatal("session outage boundary", aborted, w.Code, calls.Load(), w.Header(), w.Body.String())
			}
			if want == 503 && w.Header().Get("Retry-After") != "5" {
				t.Fatal("missing outage retry hint")
			}
		})
	}
}

func TestAIGenerationAuthorityIndependentCapabilities(t *testing.T) {
	for _, keep := range []string{"admin", "write-own", "both"} {
		t.Run(keep, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			caps := []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn}
			_, users, _, cookie, actor := dashboardAuthorityIdentity(t, s, caps)
			// Plain article generation requires neither the content API switch
			// nor activation of the Pro Max plugin.
			contentAuthorityDisable(t, s, false)
			setAIResponse(s, 200, "text/event-stream", aiFrame(aiGenerationAuthorityText, "stop")+"data: [DONE]\n\n", func(*http.Request) {
				if keep == "admin" {
					caps = caps[:1]
				} else if keep == "write-own" {
					caps = caps[1:]
				}
				if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: caps}}); err != nil {
					t.Error(err)
				}
			})
			w, aborted := commentAuthorityServe(s.routes(), aiGenerationAuthorityRequest(cookie, actor, t.Context(), aiGenerationJSON()))
			waitAIGenerationSlot(t, s, actor.UserID)
			if aborted || w.Code != 200 || strings.Contains(w.Body.String(), "event: done") != (keep == "both") || strings.Contains(w.Body.String(), aiGenerationAuthorityText) != (keep == "both") {
				t.Fatal("independent capabilities", aborted, w.Code, w.Body.String())
			}
		})
	}
}

func TestAIGenerationAuthorityEnvironmentSession(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		name := "allowed"
		if revoke {
			name = "logout"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, _, cookie, actor := newCommentHTTPTestServer(t)
			if actor.UserID != 0 {
				t.Fatal("environment administrator expected")
			}
			setAIResponse(s, 200, "text/event-stream", aiFrame(aiGenerationAuthorityText, "stop")+"data: [DONE]\n\n", func(*http.Request) {
				if revoke {
					if err := s.auth.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Error(err)
					}
				}
			})
			w, aborted := commentAuthorityServe(s.routes(), aiGenerationAuthorityRequest(cookie, actor, t.Context(), aiGenerationJSON()))
			waitAIGenerationSlot(t, s, actor.UserID)
			if aborted || w.Code != 200 || strings.Contains(w.Body.String(), "event: done") == revoke || strings.Contains(w.Body.String(), aiGenerationAuthorityText) == revoke {
				t.Fatal("environment session boundary", aborted, w.Code, w.Body.String())
			}
		})
	}
}

func TestAIGenerationAuthorityRequestProofs(t *testing.T) {
	for _, change := range []string{"origin", "csrf", "duplicate-csrf", "no-origin"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
			var calls atomic.Int32
			setAIResponse(s, 200, "text/event-stream", aiFrame(aiGenerationAuthorityText, "stop")+"data: [DONE]\n\n", func(*http.Request) { calls.Add(1) })
			r := aiGenerationAuthorityRequest(cookie, actor, t.Context(), aiGenerationJSON())
			r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: func() {
				switch change {
				case "origin":
					r.Header.Set("Origin", "https://other.example.test")
				case "csrf":
					r.Header.Set("X-CSRF-Token", "invalid")
				case "duplicate-csrf":
					r.Header.Add("X-CSRF-Token", actor.CSRFToken)
				case "no-origin":
					r.Header.Del("Origin")
				}
			}}
			w, aborted := commentAuthorityServe(s.routes(), r)
			waitAIGenerationSlot(t, s, actor.UserID)
			want, wantCalls := 403, int32(0)
			if change == "no-origin" {
				want, wantCalls = 200, 1
			}
			if aborted || w.Code != want || calls.Load() != wantCalls {
				t.Fatal("request proof boundary", aborted, w.Code, calls.Load())
			}
		})
	}
}
