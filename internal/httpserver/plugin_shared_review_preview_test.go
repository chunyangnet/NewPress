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
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/pluginshared"
)

// All rows are deterministic synthetic fixtures. Optional PostgreSQL exercises
// the real reader/diagnostics, not worker execution or a content transaction.
func newSharedReviewPreviewStore(t *testing.T) *sharedMaintenanceHTTPStore {
	t.Helper()
	s := newSharedMaintenanceHTTPStore(t)
	s.records, s.quarantines = nil, nil
	at := time.Date(2026, 11, 25, 1, 0, 0, 0, time.UTC)
	for i := 1; i <= 23; i++ {
		job := fmt.Sprintf("%064x", i)
		id, _ := plugineffects.OperationID(job, "synthetic-slot")
		r := plugineffects.Receipt{Schema: 1, JobID: job, Plugin: "demo--reader", Slot: "synthetic-slot", ActorID: 7, Fingerprint: strings.Repeat("b", 64), ManifestDigest: strings.Repeat("c", 64), ModuleDigest: strings.Repeat("d", 64),
			Result: plugineffects.Result{OperationID: id, PostID: 9007199254740993, PreviousVersion: 9007199254740992, Version: 9007199254740993, Changed: true, CommittedAt: at.Add(time.Duration(i) * time.Minute)}}
		if i%2 == 0 {
			r.Result.Changed = false
			r.Result.Version = r.Result.PreviousVersion
		}
		raw, _ := json.Marshal(r)
		index, _ := json.Marshal(pluginshared.Index{JobID: job, Slot: r.Slot})
		s.records = append(s.records, pluginshared.Record{Type: pluginshared.ContentEffect, Key: id, Data: raw, Index: index})
	}
	for i, slug := range []string{"demo-enabled", "demo-disabled", "demo-retired"} {
		epoch, err := plugin.NewTaskEpoch(0)
		if err != nil {
			t.Fatal(err)
		}
		control := plugin.TaskControl{Schema: 1, Slug: slug, Revision: int64(i + 1), ModuleDigest: strings.Repeat("d", 64), Epoch: epoch, Enabled: i == 0, Retired: i == 2,
			Manifest:      plugin.Manifest{Schema: 1, Slug: slug, Name: "Synthetic browser fixture", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm", Hooks: map[string][]string{"task.execute.v1": {"np_handle"}}},
			Configuration: plugin.Configuration{Version: 2, Values: map[string]any{}}}
		raw, _ := json.Marshal(control)
		index, _ := json.Marshal(pluginshared.Index{Revision: control.Revision, Epoch: epoch.Token, Enabled: control.Enabled, Retired: control.Retired})
		s.records = append(s.records, pluginshared.Record{Type: pluginshared.Authority, Key: slug, Data: raw, Index: index})
	}
	for _, r := range s.records {
		if d := pluginshared.Diagnose(r); d.Fault != "" {
			t.Fatal("invalid browser fixture", d)
		}
	}
	for i := range 2 {
		r := s.records[i]
		r.Key = "synthetic-wrong-index"
		q, err := pluginshared.Capture(r, "index_mismatch", pluginshared.CaptureRequest{Type: r.Type, Reference: pluginshared.Reference(r), Key: fmt.Sprintf("synthetic-capture-%d", i), Operator: "演示维护人员", Reason: `<img src=x onerror=alert(1)> 仅用于浏览器文本转义核对`}, at.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			_, audit, err := pluginshared.BuildRepair(q, pluginshared.RepairRequest{ID: q.ID, Operator: "演示复核人员", Reason: "合成夹具：已核对原结果"}, at.Add(2*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			q.Repair = &audit
		}
		s.quarantines = append(s.quarantines, q)
	}
	return s
}

func TestPluginSharedReviewBrowserPreview(t *testing.T) {
	if os.Getenv("NEWPRESS_SHARED_REVIEW_BROWSER") != "1" {
		t.Skip("opt-in shared review browser fixture")
	}
	s, _, _, _ := newReviewedTaskHTTPTestServer(t)
	store := newSharedReviewPreviewStore(t)
	if os.Getenv("NEWPRESS_WEBHOOK_MAINTENANCE_BROWSER") == "1" {
		appendWebhookMaintenancePreview(t, store)
	}
	s.SetPluginTaskStore(store)
	if dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL"); dsn != "" {
		pool, err := database.Open(t.Context(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		schema := fmt.Sprintf("review_browser_%d", time.Now().UnixNano())
		quoted := pgx.Identifier{schema}.Sanitize()
		if _, err = pool.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
			t.Fatal(err)
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := pool.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
				t.Error(err)
			}
		}()
		isolated, err := database.Open(t.Context(), dsn+"&search_path="+schema)
		if err != nil {
			t.Fatal(err)
		}
		defer isolated.Close()
		if err = database.Migrate(t.Context(), isolated); err != nil {
			t.Fatal(err)
		}
		for _, r := range store.records {
			var index pluginshared.Index
			if err = json.Unmarshal(r.Index, &index); err != nil {
				t.Fatal(err)
			}
			if r.Type == pluginshared.ContentEffect {
				_, err = isolated.Exec(t.Context(), `INSERT INTO np_plugin_content_effects(id,job_id,slot,data) VALUES($1,$2,$3,$4)`, r.Key, index.JobID, index.Slot, r.Data)
			} else if r.Type == pluginshared.WebhookIntent {
				_, err = isolated.Exec(t.Context(), `INSERT INTO np_plugin_webhook_intents(id,job_id,slot,version,state,data) VALUES($1,$2,$3,$4,$5,$6)`, r.Key, index.JobID, index.Slot, index.Version, index.State, r.Data)
			} else {
				_, err = isolated.Exec(t.Context(), `INSERT INTO np_plugin_task_authorizations(slug,revision,epoch,enabled,retired,data) VALUES($1,$2,$3,$4,$5,$6)`, r.Key, index.Revision, index.Epoch, index.Enabled, index.Retired, r.Data)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, q := range store.quarantines {
			raw, _ := json.Marshal(q)
			if _, err = isolated.Exec(t.Context(), `INSERT INTO np_plugin_shared_quarantines(id,record_type,reference,captured_at,repaired,data) VALUES($1,$2,$3,$4,$5,$6)`, q.ID, q.Original.Type, q.Reference, q.CapturedAt, q.Repair != nil, raw); err != nil {
				t.Fatal(err)
			}
		}
		s.SetPluginTaskStore(database.NewPluginJobRepository(isolated))
		t.Log("synthetic rows in isolated PostgreSQL schema; real repository reads; no content write or worker")
	} else {
		t.Log("synthetic in-memory reader; no PostgreSQL or worker")
	}
	address := os.Getenv("NEWPRESS_SHARED_REVIEW_ADDR")
	if address == "" {
		address = "127.0.0.1:19371"
	}
	if host, _, err := net.SplitHostPort(address); err != nil || host != "127.0.0.1" {
		t.Fatal("preview address must be IPv4 loopback")
	}
	s.config.PublicURL = "http://" + address
	server := &http.Server{Addr: address, ReadHeaderTimeout: 5 * time.Second}
	application := s.routes()
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__shared-review/finish" && r.Method == http.MethodPost {
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
	t.Cleanup(func() { _ = server.Close() })
	t.Log("Shared review preview: " + s.config.PublicURL + "/admin/plugin-records/; persistent temporary reviewer account")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

// Opt-in synthetic records exercise all new projections; no sender runs here.
func appendWebhookMaintenancePreview(t *testing.T, store *sharedMaintenanceHTTPStore) {
	t.Helper()
	at := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	build := func(state, slot string) pluginshared.Record {
		t.Helper()
		r := plugindispatch.Request{Authorization: plugineffects.Authorization{Claim: pluginjobs.Job{ID: strings.Repeat("7", 64)}, ManifestDigest: strings.Repeat("b", 64), ModuleDigest: strings.Repeat("c", 64)}, Slot: slot, URL: "https://example.com/PRIVATE-PATH?token=PRIVATE-TOKEN", Body: json.RawMessage(`{"private":"PRIVATE-BODY"}`)}
		id, fp, body, err := r.Identity()
		if err != nil {
			t.Fatal(err)
		}
		i := plugindispatch.Intent{Schema: 1, ID: id, JobID: r.Authorization.Claim.ID, SourceFingerprint: strings.Repeat("d", 64), Plugin: "webhook-preview", Kind: "task.execute.v1", Epoch: strings.Repeat("e", 64), ActorID: 7, ManifestDigest: r.Authorization.ManifestDigest, ModuleDigest: r.Authorization.ModuleDigest, Slot: slot, Fingerprint: fp, URL: r.URL, Body: body, Version: 1, State: state, CreatedAt: at, UpdatedAt: at}
		if state == "cancelled" {
			i.Version = 2
			i.Outcome = plugindispatch.Outcome{Code: "maintenance"}
		} else if state != "pending" {
			i.Version = 2
			i.AttemptAt = at
			i.AttemptUntil = at.Add(plugindispatch.AttemptDuration)
			i.AttemptToken = strings.Repeat("f", 64)
			if state != "sending" {
				i.Version = 3
				i.ResultAt = at
				i.Outcome = plugindispatch.Outcome{Code: "maintenance"}
			}
			if state == "http_confirmed" {
				i.Outcome = plugindispatch.Outcome{Code: "http_2xx", HTTPStatus: 204, ResponseDigest: plugineffects.Digest(nil)}
			}
			if strings.HasPrefix(state, "reviewed_") {
				i.Version = 4
				i.Review = &plugindispatch.Review{Decision: strings.TrimPrefix(state, "reviewed_"), ActorID: 7, Reason: "synthetic review", At: at}
			}
		}
		if plugindispatch.Validate(i) != nil {
			t.Fatal("invalid preview intent", state)
		}
		raw, _ := json.Marshal(i)
		index, _ := json.Marshal(pluginshared.Index{JobID: i.JobID, Slot: i.Slot, Version: i.Version, State: i.State})
		return pluginshared.Record{Type: pluginshared.WebhookIntent, Key: id, Data: raw, Index: index}
	}
	for _, state := range []string{"pending", "sending", "cancelled", "uncertain", "http_confirmed", "reviewed_received", "reviewed_not_received"} {
		store.records = append(store.records, build(state, state))
	}
	bad := build("pending", "damaged")
	bad.Data = []byte(`{`)
	store.records = append(store.records, bad)
	for n, state := range []string{"pending", "sending"} {
		r := build(state, "isolated-"+state)
		r.Key = strings.Repeat("9", 64)
		q, err := pluginshared.Capture(r, "index_mismatch", pluginshared.CaptureRequest{Type: r.Type, Reference: pluginshared.Reference(r), Key: "preview-" + state, Operator: "演示维护人员", Reason: "仅浏览器合成记录：核对发送身份和索引"}, at.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			out, audit, err := pluginshared.BuildRepair(q, pluginshared.RepairRequest{ID: q.ID, Operator: "演示复核人员", Reason: "已停止旧发送，保留待核对事实"}, at.Add(2*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			q.Repair = &audit
			store.records = append(store.records, out)
		}
		store.quarantines = append(store.quarantines, q)
	}
}
