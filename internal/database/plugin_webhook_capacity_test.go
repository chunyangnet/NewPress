package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
)

// Opt in for measured repository work against a disposable local PostgreSQL.
// Timing is evidence, not a portable assertion or production throughput claim.
func TestPostgreSQLWebhookCapacityMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_WEBHOOK_CAPACITY") != "1" {
		t.Skip("opt-in Webhook capacity measurements")
	}
	for _, scenario := range []struct {
		name string
		rows int
		body int
	}{{"small", 1, 0}, {"thousand", 1000, 0}, {"near-budget-small", 0, 0}, {"near-budget-large", 0, 16000}} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newWebhookFixture(t)
			if scenario.body > 0 {
				f.req.Body = json.RawMessage(`{"data":"` + strings.Repeat("x", scenario.body) + `"}`)
			}
			accepted := f.accept(t, "notify")
			var raw []byte
			if err := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, accepted.OperationID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var original plugindispatch.Intent
			if json.Unmarshal(raw, &original) != nil {
				t.Fatal("invalid seed")
			}
			count := scenario.rows
			if count == 0 {
				count = min(plugindispatch.MaxRecords, plugindispatch.MaxTotalBytes/plugindispatch.BudgetBytes(original))
			}
			rows := make([][]any, 0, count-1)
			for n := 1; n < count; n++ {
				i := original
				i.JobID = fmt.Sprintf("%064x", n)
				i.ID, _ = plugindispatch.OperationID(i.JobID, i.Slot)
				i.State, i.Version = "cancelled", 2
				i.Outcome = plugindispatch.Outcome{Code: "maintenance"}
				if plugindispatch.Validate(i) != nil {
					t.Fatal("invalid retained seed")
				}
				data, _ := json.Marshal(i)
				rows = append(rows, []any{i.ID, i.JobID, i.Slot, i.Version, i.State, data})
			}
			if _, err := f.pool.CopyFrom(f.ctx, pgx.Identifier{"np_plugin_webhook_intents"}, []string{"id", "job_id", "slot", "version", "state", "data"}, pgx.CopyFromRows(rows)); err != nil {
				t.Fatal(err)
			}
			f.exec(t, `ANALYZE np_plugin_webhook_intents`)
			var stored int64
			if err := f.pool.QueryRow(f.ctx, `SELECT sum(octet_length(data)) FROM np_plugin_webhook_intents`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			type sample struct {
				Operation string  `json:"operation"`
				Millis    float64 `json:"milliseconds"`
				Allocated uint64  `json:"allocated_bytes"`
				Error     string  `json:"error,omitempty"`
			}
			samples := []sample{}
			measure := func(operation string, fn func() error, allowTimeout bool) {
				t.Helper()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				start := time.Now()
				err := fn()
				elapsed := time.Since(start)
				runtime.ReadMemStats(&after)
				s := sample{Operation: operation, Millis: float64(elapsed.Microseconds()) / 1000, Allocated: after.TotalAlloc - before.TotalAlloc}
				if err != nil {
					s.Error = err.Error()
					if !allowTimeout {
						t.Fatal(operation, err)
					}
				}
				samples = append(samples, s)
			}
			measure("ready", func() error { return f.webhooks.Ready(f.ctx) }, false)
			measure("replay-250ms", func() error {
				ctx, cancel := context.WithTimeout(f.ctx, 250*time.Millisecond)
				defer cancel()
				got, err := f.webhooks.Enqueue(ctx, f.req)
				if err == nil && got != accepted {
					t.Fatal("replay identity changed")
				}
				return err
			}, true)
			measure("replay", func() error {
				got, err := f.webhooks.Enqueue(f.ctx, f.req)
				if err == nil && got != accepted {
					t.Fatal("replay identity changed")
				}
				return err
			}, false)
			if scenario.rows == 0 {
				measure("capacity-new", func() error {
					r := f.req
					r.Slot = "new-at-capacity"
					_, err := f.webhooks.Enqueue(f.ctx, r)
					if !errors.Is(err, plugindispatch.ErrCapacity) {
						t.Fatal("warm collection must preserve remaining byte reservation", err)
					}
					return nil
				}, false)
			}
			for range 3 {
				measure("list", func() error {
					page, err := f.webhooks.List(f.ctx, plugindispatch.Query{Limit: 20})
					if err == nil && (page.Total != count || len(page.Items) != min(20, count)) {
						t.Fatal("retained rows missing")
					}
					return err
				}, false)
				measure("idle-claim", func() error {
					_, ok, err := f.webhooks.Claim(f.ctx)
					if ok {
						t.Fatal("unfinished source was claimed")
					}
					return err
				}, false)
			}
			report, _ := json.Marshal(map[string]any{"scenario": scenario.name, "rows": count, "body_bytes": len(original.Body), "stored_bytes": stored, "reserved_bytes": count * plugindispatch.BudgetBytes(original), "limit_bytes": plugindispatch.MaxTotalBytes, "gomaxprocs": runtime.GOMAXPROCS(0), "samples": samples, "scope": "synthetic retained terminal records plus one live source; local database; no network sender; allocation deltas include driver and process activity"})
			t.Log("WEBHOOK_CAPACITY " + string(report))
		})
	}
}

// This exercises the actual installed WASM and its unchanged 250 ms host
// budget, after normal readiness has parsed retained evidence once.
func TestPostgreSQLWebhookCapacityWASMAdmission(t *testing.T) {
	if os.Getenv("NEWPRESS_WEBHOOK_CAPACITY") != "1" {
		t.Skip("opt-in Webhook capacity WASM acceptance")
	}
	for _, bodySize := range []int{47, 16011} {
		t.Run(fmt.Sprintf("body-%d", bodySize), func(t *testing.T) {
			f, executor, claim := prepareWebhookWASMAdmission(t)
			r := f.req
			r.Body = json.RawMessage(`{"data":"` + strings.Repeat("x", bodySize-11) + `"}`)
			_, fp, body, err := r.Identity()
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC().Add(-time.Hour)
			original := plugindispatch.Intent{Schema: 1, Plugin: claim.Plugin, Kind: claim.Kind, ActorID: claim.ActorID, Epoch: claim.Epoch,
				SourceFingerprint: strings.Repeat("d", 64), ManifestDigest: r.Authorization.ManifestDigest, ModuleDigest: r.Authorization.ModuleDigest,
				Slot: r.Slot, URL: r.URL, Fingerprint: fp, Body: body, Version: 2, State: "cancelled", Outcome: plugindispatch.Outcome{Code: "maintenance"}, CreatedAt: at, UpdatedAt: at}
			count := plugindispatch.MaxTotalBytes/plugindispatch.BudgetBytes(original) - 2
			rows := make([][]any, 0, count)
			for n := range count {
				i := original
				i.JobID = fmt.Sprintf("%064x", n+1)
				i.ID, _ = plugindispatch.OperationID(i.JobID, i.Slot)
				if plugindispatch.Validate(i) != nil {
					t.Fatal("invalid retained seed")
				}
				raw, _ := json.Marshal(i)
				rows = append(rows, []any{i.ID, i.JobID, i.Slot, i.Version, i.State, raw})
			}
			if _, err = f.pool.CopyFrom(f.ctx, pgx.Identifier{"np_plugin_webhook_intents"}, []string{"id", "job_id", "slot", "version", "state", "data"}, pgx.CopyFromRows(rows)); err != nil {
				t.Fatal(err)
			}
			if err = f.webhooks.Ready(f.ctx); err != nil {
				t.Fatal(err)
			}
			durations := []float64{}
			for range 2 {
				start := time.Now()
				if err = executor.Execute(f.ctx, claim); err != nil {
					t.Fatal("near-capacity actual WASM admission or replay", err)
				}
				durations = append(durations, float64(time.Since(start).Microseconds())/1000)
			}
			if err = f.jobs.Finish(f.ctx, claim, "", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			i := f.claim(t)
			if i.JobID != claim.ID || i.Slot != "wasm-notify" {
				t.Fatal("claimed a retained terminal or changed identity")
			}
			if err = f.webhooks.Finish(f.ctx, i, plugindispatch.Outcome{Code: "http_2xx", HTTPStatus: 204, ResponseDigest: plugineffects.Digest(nil)}); err != nil {
				t.Fatal(err)
			}
			f.assertNoClaim(t)
			var actual int
			if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM np_plugin_webhook_intents`).Scan(&actual); err != nil || actual != count+1 {
				t.Fatal("WASM replay created another intent", actual, err)
			}
			report, _ := json.Marshal(map[string]any{"retained_rows": count, "body_bytes": len(body), "reserved_bytes": count * plugindispatch.BudgetBytes(original), "wasm_milliseconds": durations, "new_intents": 1, "claims": 1, "sender": "none; finish outcome injected to test retained database result only"})
			t.Log("WEBHOOK_WASM_CAPACITY " + string(report))
		})
	}
}
