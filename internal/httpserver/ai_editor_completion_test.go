package httpserver

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
)

type aiTerminalRecorder struct {
	*httptest.ResponseRecorder
	terminal chan struct{}
	once     sync.Once
}

func (w *aiTerminalRecorder) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "event: done\n") || strings.Contains(string(data), "event: error\n") {
		w.once.Do(func() { close(w.terminal) })
	}
	return w.ResponseRecorder.Write(data)
}

func TestAIEditorTerminalEventWaitsForWorkerCleanup(t *testing.T) {
	for _, complete := range []bool{true, false} {
		name, terminal := "success", "done"
		body := aiFrame("Generated article.", "stop") + "data: [DONE]\n\n"
		if !complete {
			name, terminal = "interrupted", "error"
			body = aiFrame("Partial article.", "")
		}
		t.Run(name, func(t *testing.T) {
			s, handler, _, _, cookie, principal := newPersistentAdminTestServer(t)
			setAIResponse(s, 200, "text/event-stream", body, nil)
			recording, resume := make(chan struct{}), make(chan struct{})
			var started, resumed sync.Once
			unblock := func() { resumed.Do(func() { close(resume) }) }
			defer unblock()
			store, err := aieditor.NewTelemetryStore("", func() aieditor.TelemetryConfig {
				started.Do(func() { close(recording) })
				<-resume
				return aieditor.DefaultTelemetryConfig()
			})
			if err != nil {
				t.Fatal(err)
			}
			s.SetAITelemetry(store)
			request := httptest.NewRequest("POST", "https://example.test/api/v1/admin/ai/generate", strings.NewReader(aiGenerationJSON()))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", "https://example.test")
			request.Header.Set("X-CSRF-Token", principal.CSRFToken)
			request.AddCookie(cookie)
			response := &aiTerminalRecorder{ResponseRecorder: httptest.NewRecorder(), terminal: make(chan struct{})}
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				handler.ServeHTTP(response, request)
			}()
			select {
			case <-recording:
			case <-time.After(5 * time.Second):
				t.Fatal("worker did not reach cleanup")
			}
			select {
			case <-response.terminal:
				t.Error("terminal event arrived before worker cleanup")
			case <-time.After(50 * time.Millisecond):
			}
			unblock()
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("stream did not finish after cleanup")
			}
			if !strings.Contains(response.Body.String(), "event: "+terminal) {
				t.Fatal("missing terminal event", response.Body.String())
			}
			if !s.aiState.acquire(principal.UserID) {
				t.Fatal("completed generation still occupies the user's slot")
			}
			s.aiState.release(principal.UserID)
			report := store.Report(aieditor.UsageFilter{Start: "2000-01-01", End: "2100-01-01"})
			if report.Totals.Requests != 1 {
				t.Fatal("terminal event did not include completed accounting", report.Totals)
			}
		})
	}
}
