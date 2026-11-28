package aieditor

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestModelsCheckedBeforeEveryProtocolRequest(t *testing.T) {
	for _, protocol := range []string{"", ProtocolAnthropic, ProtocolGeminiText, ProtocolOpenAIImages} {
		for _, approved := range []bool{false, true} {
			name := protocol + "/public"
			if approved {
				name = protocol + "/approved-local"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				var calls atomic.Int32
				const payload = `{"data":[{"id":"gpt-image-1"}],"models":[{"name":"models/gemini-test"}]}`
				client := fakeClient(t, 200, "application/json", payload, func(*http.Request) { calls.Add(1) })
				connection := testConnection()
				connection.Protocol = protocol
				if approved {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(payload))
					}))
					defer server.Close()
					connection.BaseURL = server.URL
					var err error
					connection, err = ApproveProbeConnection(connection)
					if err != nil {
						t.Fatal(err)
					}
				}
				denied, checks := errors.New("model discovery permission revoked"), 0
				_, err := client.ModelsChecked(t.Context(), connection, func() error { checks++; return denied })
				if !errors.Is(err, denied) || checks != 1 || calls.Load() != 0 {
					t.Fatal("denied check reached model service", err, checks, calls.Load())
				}
				// The private hook must not stick to the caller's connection or client.
				models, err := client.Models(t.Context(), connection)
				if err != nil || len(models) != 1 || calls.Load() != 1 {
					t.Fatal("checked discovery contaminated ordinary client", err, len(models), calls.Load())
				}
			})
		}
	}
}

func TestModelsCheckedConcurrentClientIsolation(t *testing.T) {
	var calls atomic.Int32
	client := fakeClient(t, 200, "application/json", `{"data":[{"id":"model-a"}]}`, func(*http.Request) { calls.Add(1) })
	connection := testConnection()
	denied := errors.New("one caller denied")
	var workers sync.WaitGroup
	for i := range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if i%2 == 0 {
				if _, err := client.ModelsChecked(t.Context(), connection, func() error { return denied }); !errors.Is(err, denied) {
					t.Error("checked request lost denial", err)
				}
			} else if models, err := client.Models(t.Context(), connection); err != nil || len(models) != 1 {
				t.Error("ordinary request inherited another caller's guard", err)
			}
		}()
	}
	workers.Wait()
	if calls.Load() != 8 {
		t.Fatal("guard was shared between callers", calls.Load())
	}
}
