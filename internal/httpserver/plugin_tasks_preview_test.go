package httpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/pluginjobs"
)

// Opt-in loopback-only preview using the real router, templates, assets and
// queue. All fixture storage is temporary. No authentication bypass is added.
func TestPluginTasksBrowserPreview(t *testing.T) {
	if os.Getenv("NEWPRESS_TASK_BROWSER_PREVIEW") != "1" {
		t.Skip("opt-in browser fixture")
	}
	s, _, _, _, _ := newCommentHTTPTestServer(t)
	installTaskFixture(t, s, "background-reader.wasm")
	users, err := auth.NewFileUserStore(t.TempDir() + "/users.json")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("comment moderation password 2026")
	if err != nil {
		t.Fatal(err)
	}
	s.auth, err = auth.NewWithContext(t.Context(), auth.Config{Username: "moderator", PasswordHash: hash, Users: users, BootstrapEmail: "moderator@example.test", AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := pluginjobs.NewFileBackend(t.TempDir() + "/tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	queue := &pluginjobs.Queue{Backend: backend}
	s.SetPluginTaskStore(queue)
	workerCtx, workerStop := context.WithCancel(t.Context())
	workerDone := make(chan error, 1)
	worker := &pluginjobs.Worker{Queue: queue, Executor: s.PluginTaskExecutor()}
	go func() { workerDone <- worker.Run(workerCtx) }()
	address := os.Getenv("NEWPRESS_TASK_BROWSER_ADDR")
	if address == "" {
		address = "127.0.0.1:19367"
	}
	if host, _, err := net.SplitHostPort(address); err != nil || host != "127.0.0.1" {
		t.Fatal("preview address must be an IPv4 loopback address")
	}
	s.config.PublicURL = "http://" + address
	server := &http.Server{Addr: address, ReadHeaderTimeout: 5 * time.Second}
	application := s.routes()
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Test-only authenticated shutdown allows an automated preview to finish
		// and report a real test result without terminating the Go test process.
		if r.URL.Path == "/__task-preview/finish" && r.Method == http.MethodPost {
			if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage); !ok {
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = server.Shutdown(ctx)
			}()
			return
		}
		application.ServeHTTP(w, r)
	})
	t.Cleanup(func() {
		workerStop()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		select {
		case err := <-workerDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error("preview worker", err)
			}
		case <-ctx.Done():
			t.Error("preview worker did not stop")
		}
	})
	t.Log("Task preview: " + s.config.PublicURL + "/admin/plugins/task-fixture/ (fixture account moderator; real background-reader worker running)")
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
