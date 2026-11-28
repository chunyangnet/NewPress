package httpserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aiworkbench"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func TestAIWorkbenchAuthorityGeneration(t *testing.T) {
	for _, boundary := range []string{"before-ready", "after-ready", "upstream", "completion"} {
		for _, change := range []string{"logout", "permission", "module-restarted", "outage", "cancel", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				seedWorkbench(t, s)
				preview, err := s.aiPreviews.Put(actor.UserID, aiworkbench.Preview{Task: "summary", Context: "material"}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var changed atomic.Bool
				after := func() {
					changed.Store(true)
					aiUsageChange(t, s, manager, users, fault, cookie, change, "ai", cancel)
				}
				var calls atomic.Int32
				s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					if boundary == "upstream" {
						after()
					}
					body := &aiGenerationTrackedBody{ReadCloser: io.NopCloser(strings.NewReader(aiFrame(workbenchAuthorityMarker, "stop") + "data: [DONE]\n\n"))}
					if boundary == "completion" {
						body.afterClose = after
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
				})}}
				w := &aiGenerationBoundaryWriter{ResponseRecorder: httptest.NewRecorder()}
				if boundary == "before-ready" {
					w.deadline = after
				}
				if boundary == "after-ready" {
					w.flush = func() error { runAutosaveBoundary(&after); return nil }
				}
				aborted := serveAIGenerationBoundary(s.routes(), w, workbenchAuthorityRequest("generate", cookie, actor, ctx, workbenchGeneration(preview.Token)))
				waitAIGenerationSlot(t, s, actor.UserID)
				wantCalls := int32(1)
				if change != "allowed" && (boundary == "before-ready" || boundary == "after-ready") {
					wantCalls = 0
				}
				if !changed.Load() || calls.Load() != wantCalls {
					t.Fatal("generation boundary bypassed", changed.Load(), calls.Load(), wantCalls)
				}
				if (len(s.aiReports.List(actor.UserID, time.Now())) > 0) != (change == "allowed") {
					t.Fatal("denied generation persisted a report")
				}
				if change == "allowed" {
					if aborted || !strings.Contains(w.Body.String(), "event: done") || !strings.Contains(w.Body.String(), workbenchAuthorityMarker) {
						t.Fatal("authorized stream failed", aborted, w.Body.String())
					}
					return
				}
				if strings.Contains(w.Body.String(), "event: done") || strings.Contains(w.Body.String(), "PRIVATE-SESSION-DETAIL") {
					t.Fatal("revoked stream returned private completion", w.Body.String())
				}
				if boundary != "completion" && strings.Contains(w.Body.String(), workbenchAuthorityMarker) {
					t.Fatal("revoked model output escaped")
				}
				if change == "cancel" {
					if boundary == "before-ready" && (!aborted || w.Body.Len() != 0) || strings.Contains(w.Body.String(), "event: error") {
						t.Fatal("cancelled stream wrote response", aborted, w.Body.String())
					}
					return
				}
				if boundary == "before-ready" {
					want := map[string]int{"logout": 401, "permission": 403, "module-restarted": 409, "outage": 503}[change]
					if aborted || w.Code != want || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
						t.Fatal("first-frame denial did not preserve JSON semantics", aborted, w.Code, want)
					}
				} else if aborted || !strings.Contains(w.Body.String(), "event: error") {
					t.Fatal("revoked stream lacks terminal error", aborted, w.Body.String())
				}
			})
		}
	}
}

type workbenchWaitingRepository struct {
	content.Repository
	entered, cancelled chan struct{}
}

func (r *workbenchWaitingRepository) ListPublishedPage(ctx context.Context, _, _ int) (content.PublishedPage, error) {
	close(r.entered)
	<-ctx.Done()
	close(r.cancelled)
	return content.PublishedPage{}, ctx.Err()
}

func TestAIWorkbenchAuthoritySilentWait(t *testing.T) {
	for _, boundary := range []string{"preview", "headers", "body"} {
		for _, change := range []string{"logout", "permission", "module-restarted", "outage", "cancel"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, repo, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				seedWorkbench(t, s)
				preview, err := s.aiPreviews.Put(actor.UserID, aiworkbench.Preview{Task: "summary", Context: "original"}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				entered, cancelled, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
				release, closed := make(chan struct{}), make(chan struct{})
				defer close(release)
				s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
					if boundary == "headers" {
						close(entered)
						select {
						case <-r.Context().Done():
							close(cancelled)
						case <-release:
						}
						return nil, context.Canceled
					}
					reader, writer := io.Pipe()
					go func() {
						_, _ = io.WriteString(writer, ": upstream waiting\n\n")
						close(entered)
						select {
						case <-r.Context().Done():
							close(cancelled)
						case <-release:
						}
						_ = writer.CloseWithError(context.Canceled)
					}()
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &aiGenerationTrackedBody{ReadCloser: reader, afterClose: func() { close(closed) }}}, nil
				})}}
				action, input := "generate", workbenchGeneration(preview.Token)
				if boundary == "preview" {
					action, input = "preview", `{"task":"seo","days":7}`
					s.repository = &workbenchWaitingRepository{Repository: repo, entered: entered, cancelled: cancelled}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				w := httptest.NewRecorder()
				var aborted bool
				go func() {
					defer close(finished)
					aborted = serveAIGenerationBoundary(s.routes(), w, workbenchAuthorityRequest(action, cookie, actor, ctx, input))
				}()
				t.Cleanup(func() {
					cancel()
					waitAIGenerationSignal(t, finished, 10*time.Second, "workbench handler cleanup stalled")
					waitAIGenerationSlot(t, s, actor.UserID)
				})
				waitAIGenerationSignal(t, entered, 4*time.Second, "workbench did not enter blocking operation")
				aiUsageChange(t, s, manager, users, fault, cookie, change, "ai", cancel)
				waitAIGenerationSignal(t, cancelled, 4*time.Second, "workbench continued after revocation")
				waitAIGenerationSignal(t, finished, 2*time.Second, "workbench did not finish")
				waitAIGenerationSlot(t, s, actor.UserID)
				if boundary == "body" {
					waitAIGenerationSignal(t, closed, 2*time.Second, "upstream body not closed")
				}
				if len(s.aiReports.List(actor.UserID, time.Now())) != 0 || strings.Contains(w.Body.String(), "event: done") || strings.Contains(w.Body.String(), "PRIVATE-") {
					t.Fatal("revoked wait exposed or saved report")
				}
				if change == "cancel" {
					if boundary == "preview" && (!aborted || w.Body.Len() != 0) || strings.Contains(w.Body.String(), "event: error") {
						t.Fatal("cancelled wait returned output")
					}
					return
				}
				if boundary == "preview" {
					want := map[string]int{"logout": 401, "permission": 403, "module-restarted": 409, "outage": 503}[change]
					if aborted || w.Code != want {
						t.Fatal("preview waiting response", aborted, w.Code, want)
					}
					if _, err := s.aiPreviews.Take(actor.UserID, preview.Token, time.Now()); err != nil {
						t.Fatal("revoked preview replaced prior token", err)
					}
				} else if aborted || !strings.Contains(w.Body.String(), "event: error") {
					t.Fatal("stream waiting response", aborted, w.Body.String())
				}
			})
		}
	}
}

func TestAIWorkbenchAuthorityFailedWrite(t *testing.T) {
	for _, boundary := range []string{"ready-write", "ready-flush", "delta-write", "done-flush"} {
		t.Run(boundary, func(t *testing.T) {
			s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
			seedWorkbench(t, s)
			preview, err := s.aiPreviews.Put(actor.UserID, aiworkbench.Preview{Task: "summary", Context: "material"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			setAIResponse(s, 200, "text/event-stream", aiFrame(workbenchAuthorityMarker, "stop")+"data: [DONE]\n\n", func(*http.Request) { calls.Add(1) })
			w := &aiGenerationBoundaryWriter{ResponseRecorder: httptest.NewRecorder()}
			failed := false
			w.write = func(data []byte) (int, error) {
				if boundary == "ready-write" || boundary == "delta-write" && strings.Contains(string(data), "event: delta") {
					failed = true
					return 0, io.ErrShortWrite
				}
				return w.ResponseRecorder.Write(data)
			}
			w.flush = func() error {
				if boundary == "ready-flush" || boundary == "done-flush" && strings.Contains(w.Body.String(), "event: done") {
					failed = true
					return errors.New("synthetic write failure")
				}
				return nil
			}
			aborted := serveAIGenerationBoundary(s.routes(), w, workbenchAuthorityRequest("generate", cookie, actor, t.Context(), workbenchGeneration(preview.Token)))
			waitAIGenerationSlot(t, s, actor.UserID)
			if !aborted || !failed || strings.HasPrefix(boundary, "ready") && calls.Load() != 0 {
				t.Fatal("failed write did not abort/release slot", aborted, failed, calls.Load())
			}
		})
	}
}

func TestAIWorkbenchAuthoritySlotUntilWorkerStops(t *testing.T) {
	s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
	seedWorkbench(t, s)
	preview, err := s.aiPreviews.Put(actor.UserID, aiworkbench.Preview{Task: "summary", Context: "material"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	entered, cancelled, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	defer stop()
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
		serveAIGenerationBoundary(s.routes(), httptest.NewRecorder(), workbenchAuthorityRequest("generate", cookie, actor, ctx, workbenchGeneration(preview.Token)))
	}()
	t.Cleanup(func() {
		cancel()
		stop()
		waitAIGenerationSignal(t, finished, 5*time.Second, "workbench cleanup timed out")
		waitAIGenerationSlot(t, s, actor.UserID)
	})
	waitAIGenerationSignal(t, entered, 4*time.Second, "workbench worker did not start")
	cancel()
	waitAIGenerationSignal(t, cancelled, 2*time.Second, "worker did not receive cancellation")
	waitAIGenerationSignal(t, finished, 2*time.Second, "handler did not stop")
	if s.aiState.acquire(actor.UserID) {
		s.aiState.release(actor.UserID)
		t.Fatal("request cancellation released a still-running worker's slot")
	}
	stop()
	waitAIGenerationSlot(t, s, actor.UserID)
	if len(s.aiReports.List(actor.UserID, time.Now())) != 0 {
		t.Fatal("cancelled worker saved a report")
	}
}

func TestAIWorkbenchAuthorityPartialFrameCancellation(t *testing.T) {
	s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
	seedWorkbench(t, s)
	preview, err := s.aiPreviews.Put(actor.UserID, aiworkbench.Preview{Task: "summary", Context: "material"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	setAIResponse(s, 200, "text/event-stream", aiFrame(strings.Repeat("&", 60000), "stop")+"data: [DONE]\n\n", nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &aiGenerationBoundaryWriter{ResponseRecorder: httptest.NewRecorder()}
	changed := false
	w.afterWrite = func(data []byte) {
		if !changed && strings.HasPrefix(string(data), "event: delta") {
			changed = true
			cancel()
		}
	}
	aborted := serveAIGenerationBoundary(s.routes(), w, workbenchAuthorityRequest("generate", cookie, actor, ctx, workbenchGeneration(preview.Token)))
	waitAIGenerationSlot(t, s, actor.UserID)
	if !changed || !aborted || w.Body.Len() > 70<<10 || strings.Contains(w.Body.String(), "event: error") || strings.Contains(w.Body.String(), "event: done") {
		t.Fatal("partial frame cancellation exceeded chunk budget", changed, aborted, w.Body.Len())
	}
}
