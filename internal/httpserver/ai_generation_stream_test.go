package httpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
)

func waitAIGenerationSignal(t *testing.T, signal <-chan struct{}, timeout time.Duration, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(timeout):
		t.Fatal(message)
	}
}

func TestAIGenerationAuthoritySilentModel(t *testing.T) {
	for _, change := range []string{"logout", "permission", "module-restarted", "outage"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn})
			fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
			sessions.SessionStore = fault
			entered, cancelled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
				close(entered)
				<-r.Context().Done()
				close(cancelled)
				return nil, r.Context().Err()
			})}}
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			w := httptest.NewRecorder()
			var aborted bool
			go func() {
				defer close(finished)
				aborted = serveAIGenerationBoundary(s.routes(), w, aiGenerationAuthorityRequest(cookie, actor, ctx, aiGenerationJSON()))
			}()
			t.Cleanup(func() {
				cancel()
				waitAIGenerationSignal(t, finished, 5*time.Second, "generation handler cleanup timed out")
				waitAIGenerationSlot(t, s, actor.UserID)
			})
			waitAIGenerationSignal(t, entered, 4*time.Second, "silent model did not start")
			code := "auth.authentication_required"
			switch change {
			case "logout":
				if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
					t.Fatal(err)
				}
			case "permission":
				code = "auth.capability_required"
				if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: []string{auth.CapabilityAdminAccess}}}); err != nil {
					t.Fatal(err)
				}
			case "module-restarted":
				code = "ai.inactive"
				accountAuthorityModule(t, s, "ai", false)
				accountAuthorityModule(t, s, "ai", true)
			case "outage":
				code = "auth.session_unavailable"
				fault.fail.Store(true)
			}
			waitAIGenerationSignal(t, cancelled, 4*time.Second, "silent model continued after authority changed")
			waitAIGenerationSignal(t, finished, 2*time.Second, "revoked stream handler did not finish")
			waitAIGenerationSlot(t, s, actor.UserID)
			if aborted || !strings.Contains(w.Body.String(), code) || strings.Contains(w.Body.String(), "PRIVATE-") || strings.Contains(w.Body.String(), "event: done") {
				t.Fatal("silent generation authority response", aborted, w.Code, w.Body.String())
			}
		})
	}
}

func TestAIGenerationAuthorityStopsStream(t *testing.T) {
	for _, kind := range []string{"text", "thinking", "usage", "later-event"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, _, manager, _, cookie, actor := newPersistentAdminTestServer(t)
			frame := aiFrame(aiGenerationAuthorityText, "")
			if kind == "thinking" {
				frame = `data: {"choices":[{"index":0,"delta":{"reasoning_content":"PRIVATE-THINKING"}}]}` + "\n\n"
			} else if kind == "usage" {
				frame = `data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}` + "\n\n"
			}
			frames := &aiEditorAuthorityStream{frames: []string{frame, frame, aiFrame("end", "stop") + "data: [DONE]\n\n"}}
			wantReads := 1
			if kind == "later-event" {
				wantReads = 2
			}
			body := &aiGenerationTrackedBody{ReadCloser: io.NopCloser(frames)}
			s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
				frames.beforeRead = func(reads int) {
					if reads != wantReads {
						return
					}
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Error(err)
					}
					if kind == "later-event" {
						select {
						case <-r.Context().Done():
						case <-time.After(4 * time.Second):
							t.Error("model was not cancelled while waiting for the next event")
						}
					}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
			})}}
			w, aborted := commentAuthorityServe(s.routes(), aiGenerationAuthorityRequest(cookie, actor, t.Context(), aiGenerationJSON()))
			waitAIGenerationSlot(t, s, actor.UserID)
			if aborted || w.Code != 200 || !body.closed.Load() || frames.reads != wantReads || strings.Contains(w.Body.String(), "event: done") || !strings.Contains(w.Body.String(), "event: error") || kind != "later-event" && strings.Contains(w.Body.String(), "PRIVATE-") {
				t.Fatal("revoked stream continued", aborted, frames.reads, body.closed.Load(), w.Body.String())
			}
		})
	}
}

func TestAIGenerationAuthorityBufferedCompletion(t *testing.T) {
	s, _, manager, _, cookie, actor := newPersistentAdminTestServer(t)
	setAIResponse(s, 200, "text/event-stream", aiFrame(aiGenerationAuthorityText, "stop")+"data: [DONE]\n\n", nil)
	w := &aiGenerationBoundaryWriter{ResponseRecorder: httptest.NewRecorder()}
	changed := false
	w.afterWrite = func(p []byte) {
		if changed || !strings.HasPrefix(string(p), "event: delta") {
			return
		}
		// The HTTP writer is stopped here. Once the producer releases its
		// slot, the terminal result is queued but has not been written.
		waitAIGenerationSlot(t, s, actor.UserID)
		if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
			t.Fatal(err)
		}
		changed = true
	}
	aborted := serveAIGenerationBoundary(s.routes(), w, aiGenerationAuthorityRequest(cookie, actor, t.Context(), aiGenerationJSON()))
	if !changed || aborted || strings.Contains(w.Body.String(), "event: done") || !strings.Contains(w.Body.String(), "event: error") {
		t.Fatal("queued completion bypassed output check", changed, aborted, w.Body.String())
	}
}

func TestAIGenerationAuthorityBoundedStreamChecks(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	_, _, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, nil)
	setAIResponse(s, 200, "text/event-stream", strings.Repeat(aiFrame("x", ""), 200)+aiFrame("end", "stop")+"data: [DONE]\n\n", nil)
	before := sessions.lookups.Load()
	w, aborted := commentAuthorityServe(s.routes(), aiGenerationAuthorityRequest(cookie, actor, t.Context(), aiGenerationJSON()))
	waitAIGenerationSlot(t, s, actor.UserID)
	checks := sessions.lookups.Load() - before
	if aborted || w.Code != 200 || checks < 5 || checks > 35 || !strings.Contains(w.Body.String(), strings.Repeat("x", 200)+"end") {
		t.Fatal("stream checks not bounded or output changed", aborted, w.Code, checks)
	}
	t.Logf("201 text events: %d session lookups", checks)
}

func TestAIGenerationAuthorityLargeOutput(t *testing.T) {
	s, _, manager, _, cookie, actor := newPersistentAdminTestServer(t)
	// JSON escaping expands each '&' to six bytes. Small upstream frames
	// stay within the parser limit while output exceeds the recheck budget.
	setAIResponse(s, 200, "text/event-stream", strings.Repeat(aiFrame(strings.Repeat("&", 10<<10), ""), 40)+aiFrame("end", "stop")+"data: [DONE]\n\n", nil)
	w := &aiGenerationBoundaryWriter{ResponseRecorder: httptest.NewRecorder()}
	changed := false
	w.afterWrite = func([]byte) {
		if changed || w.Body.Len() < 900<<10 {
			return
		}
		if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
			t.Fatal(err)
		}
		changed = true
	}
	aborted := serveAIGenerationBoundary(s.routes(), w, aiGenerationAuthorityRequest(cookie, actor, t.Context(), aiGenerationJSON()))
	waitAIGenerationSlot(t, s, actor.UserID)
	if !changed || w.Body.Len() > privateResponseCheckBytes+1024 || strings.Contains(w.Body.String(), "event: done") || !aborted && !strings.Contains(w.Body.String(), "event: error") {
		t.Fatal("large output exceeded authority budget", changed, aborted, w.Body.Len())
	}
	if aborted && strings.Contains(w.Body.String(), "event: error") {
		t.Fatal("error appended inside incomplete data frame")
	}
}

func TestAIGenerationAuthorityWriterFailure(t *testing.T) {
	for _, boundary := range []string{"ready", "delta"} {
		for _, failure := range []string{"write", "short-write", "flush"} {
			t.Run(boundary+"/"+failure, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
				setAIResponse(s, 200, "text/event-stream", aiFrame(aiGenerationAuthorityText, "stop")+"data: [DONE]\n\n", nil)
				w := &aiGenerationBoundaryWriter{ResponseRecorder: httptest.NewRecorder()}
				failed, matched := false, false
				w.write = func(p []byte) (int, error) {
					matched = strings.HasPrefix(string(p), "event: "+boundary)
					if !failed && matched && failure != "flush" {
						failed = true
						n, _ := w.ResponseRecorder.Write(p[:len(p)/2])
						if failure == "write" {
							return n, io.ErrClosedPipe
						}
						return n, nil
					}
					return w.ResponseRecorder.Write(p)
				}
				w.flush = func() error {
					if !failed && matched && failure == "flush" {
						failed = true
						return io.ErrClosedPipe
					}
					return nil
				}
				aborted := serveAIGenerationBoundary(s.routes(), w, aiGenerationAuthorityRequest(cookie, actor, t.Context(), aiGenerationJSON()))
				waitAIGenerationSlot(t, s, actor.UserID)
				if !failed || !aborted || strings.Contains(w.Body.String(), "event: done") || strings.Contains(w.Body.String(), "event: error") {
					t.Fatal("failed writer reused or generation retained", failed, aborted, w.Body.String())
				}
			})
		}
	}
}

func TestAIGenerationAuthoritySlotHeldUntilWorkerStops(t *testing.T) {
	s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
	entered, cancelled, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		close(cancelled)
		<-release
		return nil, r.Context().Err()
	})}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		defer close(finished)
		serveAIGenerationBoundary(s.routes(), httptest.NewRecorder(), aiGenerationAuthorityRequest(cookie, actor, ctx, aiGenerationJSON()))
	}()
	t.Cleanup(func() {
		cancel()
		once.Do(func() { close(release) })
		waitAIGenerationSignal(t, finished, 5*time.Second, "generation handler cleanup timed out")
		waitAIGenerationSlot(t, s, actor.UserID)
	})
	waitAIGenerationSignal(t, entered, 4*time.Second, "worker did not start")
	cancel()
	waitAIGenerationSignal(t, cancelled, 2*time.Second, "upstream cancellation not propagated")
	waitAIGenerationSignal(t, finished, 2*time.Second, "handler did not finish after cancellation")
	if s.aiState.acquire(actor.UserID) {
		s.aiState.release(actor.UserID)
		t.Fatal("slot released before upstream worker stopped")
	}
	once.Do(func() { close(release) })
	waitAIGenerationSlot(t, s, actor.UserID)
}
