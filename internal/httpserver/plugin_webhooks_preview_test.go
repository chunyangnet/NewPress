package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
)

func TestPluginWebhookBrowserPreview(t *testing.T) {
	if os.Getenv("NEWPRESS_WEBHOOK_BROWSER") != "1" {
		t.Skip("opt-in isolated PostgreSQL browser fixture")
	}
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("temporary PostgreSQL required")
	}
	base, err := database.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal("temporary database unavailable")
	}
	defer base.Close()
	schema := fmt.Sprintf("webhook_browser_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := base.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config := base.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal("isolated database unavailable")
	}
	defer pool.Close()
	if err = database.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	s, _, _, _, _ := newCommentHTTPTestServer(t)
	password := "Webhook browser fixture password 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	s.auth, err = auth.NewWithContext(t.Context(), auth.Config{Username: "reviewer", PasswordHash: hash, Users: database.NewUserRepository(pool), BootstrapEmail: "reviewer@example.test", AbsoluteTTL: time.Hour, IdleTTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, owner, err := s.auth.AuthenticateContext(t.Context(), "webhook-preview", "reviewer", password)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.auth.CreateUser(t.Context(), owner, auth.UserInput{Username: "other-reviewer", Email: "other@example.test", Status: auth.UserStatusActive, RoleSlugs: []string{"administrator"}}, password)
	if err != nil {
		t.Fatal(err)
	}
	states := []string{"uncertain", "pending", "sending", "http_confirmed", "cancelled", "reviewed_received", "reviewed_not_received"}
	for n := 0; n < 28; n++ {
		job := pluginjobs.Job{ID: plugineffects.Digest([]byte(fmt.Sprintf("browser-job-%d", n))), Plugin: "webhook-demo", Kind: "task.execute.v1", Epoch: strings.Repeat("e", 64), ActorID: owner.UserID, Fingerprint: strings.Repeat("f", 64)}
		r := plugindispatch.Request{Authorization: plugineffects.Authorization{Claim: job, ManifestDigest: strings.Repeat("a", 64), ModuleDigest: strings.Repeat("b", 64)}, Slot: fmt.Sprintf("notify-%02d", n), URL: "https://example.com/private-path?token=must-not-display", Body: json.RawMessage(`{"message":"private body must not display","id":9007199254740993}`)}
		id, fp, body, err := r.Identity()
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC().Add(time.Duration(n-30) * time.Minute).Truncate(time.Microsecond)
		i := plugindispatch.Intent{Schema: 1, ID: id, JobID: job.ID, SourceFingerprint: job.Fingerprint, Plugin: job.Plugin, Kind: job.Kind, Epoch: job.Epoch, ActorID: owner.UserID, ManifestDigest: r.Authorization.ManifestDigest, ModuleDigest: r.Authorization.ModuleDigest, Slot: r.Slot, Fingerprint: fp, URL: r.URL, Body: body, Version: 1, State: states[n%len(states)], CreatedAt: at, UpdatedAt: at}
		if i.State == "cancelled" {
			i.Version = 2
			i.UpdatedAt = at.Add(time.Second)
			i.Outcome = plugindispatch.Outcome{Code: "source_cancelled"}
		}
		if i.State != "pending" && i.State != "cancelled" {
			i.Version = 2
			i.AttemptAt = at.Add(time.Second)
			i.AttemptUntil = i.AttemptAt.Add(plugindispatch.AttemptDuration)
			i.AttemptToken = strings.Repeat("c", 64)
			i.UpdatedAt = i.AttemptAt
			if i.State != "sending" {
				i.Version = 3
				i.UpdatedAt = at.Add(2 * time.Second)
				i.ResultAt = i.UpdatedAt
				i.Outcome = plugindispatch.Outcome{Code: "transport_unconfirmed"}
				if i.State == "http_confirmed" {
					i.Outcome = plugindispatch.Outcome{Code: "http_2xx", HTTPStatus: 202, ResponseDigest: plugineffects.Digest([]byte("accepted")), ResponseBytes: 8}
				}
				if strings.HasPrefix(i.State, "reviewed_") {
					i.Version = 4
					i.UpdatedAt = at.Add(3 * time.Second)
					i.Review = &plugindispatch.Review{Decision: strings.TrimPrefix(i.State, "reviewed_"), Reason: "已向接收方核对测试操作 ID", ActorID: owner.UserID, At: i.UpdatedAt}
				}
			}
		}
		if err = plugindispatch.Validate(i); err != nil {
			t.Fatal(i.State, err)
		}
		raw, _ := json.Marshal(i)
		if _, err = pool.Exec(t.Context(), `INSERT INTO np_plugin_webhook_intents(id,job_id,slot,version,state,data) VALUES($1,$2,$3,$4,$5,$6)`, i.ID, i.JobID, i.Slot, i.Version, i.State, raw); err != nil {
			t.Fatal(err)
		}
	}
	s.SetPluginWebhookStore(database.NewPluginWebhookRepository(pool, false))
	address := os.Getenv("NEWPRESS_WEBHOOK_ADDR")
	if address == "" {
		address = "127.0.0.1:19372"
	}
	if host, _, err := net.SplitHostPort(address); err != nil || host != "127.0.0.1" {
		t.Fatal("loopback required")
	}
	s.config.PublicURL = "http://" + address
	server := &http.Server{Addr: address, ReadHeaderTimeout: 5 * time.Second}
	app := s.routes()
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && (r.URL.Path == "/__webhook/finish" || r.URL.Path == "/__webhook/revoke-other") {
			if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage); !ok {
				return
			}
			if r.URL.Path == "/__webhook/revoke-other" {
				_, err := s.auth.UpdateUser(r.Context(), owner, other.ID, other.Version, auth.UserInput{Username: other.Username, Email: other.Email, Status: other.Status, Locale: other.Locale, RoleSlugs: []string{"editor"}})
				if err != nil {
					s.writeAPIError(w, r, 500, "fixture.failure", "fixture role change failed", nil)
					return
				}
			} else {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = server.Shutdown(ctx)
				}()
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		app.ServeHTTP(w, r)
	})
	t.Cleanup(func() { _ = server.Close() })
	t.Log("Webhook preview on " + s.config.PublicURL + "/admin/plugin-webhooks/; 28 synthetic intents, real PostgreSQL users, reviews and audit; no dispatcher or outbound requests")
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
