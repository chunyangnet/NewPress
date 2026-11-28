package httpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
)

func TestAIUsageAuthoritySilentBalance(t *testing.T) {
	for _, boundary := range []string{"headers", "body"} {
		for _, change := range []string{"logout", "permission", "module-restarted", "outage", "cancel"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySettingsManage})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				aiUsageBalanceSettings(t, s)
				entered, cancelled, finished, closed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				s.aiBalances.HTTP = &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
					if boundary == "headers" {
						close(entered)
						<-r.Context().Done()
						close(cancelled)
						return nil, r.Context().Err()
					}
					reader, writer := io.Pipe()
					body := &aiGenerationTrackedBody{ReadCloser: reader, afterClose: func() { close(closed) }}
					go func() {
						_, _ = io.WriteString(writer, `{"remaining":`)
						close(entered)
						<-r.Context().Done()
						_ = writer.CloseWithError(r.Context().Err())
						close(cancelled)
					}()
					return &http.Response{StatusCode: 200, Body: body}, nil
				})}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				w := httptest.NewRecorder()
				var aborted bool
				go func() {
					defer close(finished)
					aborted = serveAIGenerationBoundary(s.routes(), w, aiUsageTestRequest("balance", cookie, actor, ctx))
				}()
				t.Cleanup(func() {
					cancel()
					waitAIGenerationSignal(t, finished, 12*time.Second, "balance handler cleanup timed out")
					waitAIGenerationSignal(t, cancelled, 12*time.Second, "balance worker cleanup timed out")
				})
				waitAIGenerationSignal(t, entered, 4*time.Second, "balance did not reach waiting boundary")
				aiUsageChange(t, s, manager, users, fault, cookie, change, "ai", cancel)
				waitAIGenerationSignal(t, finished, 4*time.Second, "silent balance kept revoked handler waiting")
				waitAIGenerationSignal(t, cancelled, 2*time.Second, "unused balance upstream continued")
				if boundary == "body" {
					waitAIGenerationSignal(t, closed, 2*time.Second, "balance response body was not closed")
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("disconnected balance request returned output", aborted, w.Code)
					}
					return
				}
				want := map[string]int{"logout": 401, "permission": 403, "module-restarted": 409, "outage": 503}[change]
				if aborted || w.Code != want || strings.Contains(w.Body.String(), "PRIVATE-") {
					t.Fatal("silent balance authority response", aborted, w.Code, want)
				}
			})
		}
	}
}

func TestAIUsageAuthorityBalanceRequestProof(t *testing.T) {
	for _, change := range []string{"origin", "csrf", "duplicate-csrf"} {
		t.Run(change, func(t *testing.T) {
			s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
			aiUsageBalanceSettings(t, s)
			var calls atomic.Int32
			s.aiBalances.HTTP = &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"remaining":20,"unit":"USD"}`))}, nil
			})}
			r := aiUsageTestRequest("balance", cookie, actor, t.Context())
			r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: func() {
				switch change {
				case "origin":
					r.Header.Set("Origin", "https://unrelated.test")
				case "csrf":
					r.Header.Set("X-CSRF-Token", "stale-token")
				case "duplicate-csrf":
					r.Header.Add("X-CSRF-Token", actor.CSRFToken)
				}
			}}
			w, aborted := commentAuthorityServe(s.routes(), r)
			if aborted || w.Code != 403 || calls.Load() != 0 {
				t.Fatal("stale request proof used balance credential", aborted, w.Code, calls.Load())
			}
		})
	}
}
