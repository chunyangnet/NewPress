package plugindispatch

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookHTTPResponsesAndLostResponseAreSingleAttempt(t *testing.T) {
	for _, mode := range []string{"success", "status", "redirect", "lost", "partial", "large", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || !strings.Contains(string(body), "9007199254740993") || r.Method != "POST" || r.Header.Get("Idempotency-Key") != "" || len(r.Header.Get("X-NewPress-Operation")) != 64 {
					t.Error("request contract")
				}
				switch mode {
				case "success":
					w.WriteHeader(202)
					_, _ = w.Write([]byte(`{"accepted":true}`))
				case "status":
					w.WriteHeader(503)
				case "redirect":
					w.Header().Set("Location", "https://example.com/again")
					w.WriteHeader(307)
				case "lost":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				case "partial":
					w.Header().Set("Content-Length", "100")
					_, _ = w.Write([]byte("short"))
				case "large":
					_, _ = w.Write([]byte(strings.Repeat("x", MaxResponseBytes+1)))
				case "timeout":
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			client := server.Client()
			tr := client.Transport.(*http.Transport)
			tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			tr.DisableKeepAlives = true
			tr.DisableCompression = true
			tr.Protocols = new(http.Protocols)
			tr.Protocols.SetHTTP1(true)
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			client.Timeout = 150 * time.Millisecond
			i := sendingIntent(t)
			out := send(t.Context(), client, i)
			want := map[string]string{"success": "http_2xx", "status": "http_status", "redirect": "http_status", "lost": "transport_unconfirmed", "partial": "response_unconfirmed", "large": "response_unconfirmed", "timeout": "transport_unconfirmed"}[mode]
			if out.Code != want || !ValidOutcome(out) || calls.Load() != 1 {
				t.Fatalf("%s: outcome=%+v remote_calls=%d", mode, out, calls.Load())
			}
		})
	}
}
