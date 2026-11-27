package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/pluginjobs"
)

func newPluginJobsIntegrationStore(t *testing.T) (context.Context, *pgxpool.Pool, *PluginJobRepository) {
	t.Helper()
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	schema := fmt.Sprintf("pluginjobs_integrity_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	config := pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(isolated.Close)
	if err = Migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	return ctx, isolated, NewPluginJobRepository(isolated)
}

func TestPostgreSQLPluginJobsRejectsMismatchedRows(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	// Preserve sub-microsecond JSON timestamps while checking SQL's precision.
	now := time.Now().UTC().Truncate(time.Microsecond).Add(123 * time.Nanosecond)
	for _, field := range []string{"id", "plugin", "status", "available_at", "updated_at", "lease_until"} {
		t.Run(field, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `TRUNCATE np_plugin_jobs`); err != nil {
				t.Fatal(err)
			}
			in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: field, ActorID: 1, Payload: json.RawMessage(`{}`)}
			job, err := r.Enqueue(ctx, in, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.List(ctx, pluginjobs.Query{Plugin: in.Plugin}); err != nil {
				t.Fatal("valid timestamp precision rejected", err)
			}
			rowID, rowPlugin := job.ID, job.Plugin
			var value any
			switch field {
			case "id":
				rowID = strings.Repeat("0", 64)
				value = rowID
			case "plugin":
				rowPlugin = "another-reader"
				value = rowPlugin
			case "status":
				value = "retry"
			default:
				value = now.Add(time.Second)
			}
			if _, err := pool.Exec(ctx, "UPDATE np_plugin_jobs SET "+field+"=$2 WHERE id=$1", job.ID, value); err != nil {
				t.Fatal(err)
			}
			claim := job
			claim.ID = rowID
			checks := []struct {
				name string
				run  func() error
			}{
				{"list", func() error { _, err := r.List(ctx, pluginjobs.Query{Plugin: rowPlugin}); return err }},
				{"claim", func() error { _, _, err := r.Claim(ctx, now.Add(2*time.Second)); return err }},
				{"check_claim", func() error { return r.CheckClaim(ctx, claim, now.Add(2*time.Second)) }},
				{"change", func() error { return r.Change(ctx, rowPlugin, rowID, job.Version, "cancel", now.Add(2*time.Second)) }},
				{"restore", func() error { return r.CancelRestored(ctx, now.Add(2*time.Second)) }},
			}
			// Enqueue only reads its own ID; a renamed SQL row is not that row.
			if field != "id" {
				checks = append(checks, struct {
					name string
					run  func() error
				}{"replay", func() error { _, err := r.Enqueue(ctx, in, now); return err }})
			}
			for _, check := range checks {
				t.Run(check.name, func(t *testing.T) {
					if err := check.run(); !errors.Is(err, errPluginTaskRow) {
						t.Fatalf("inconsistent SQL row accepted: %v", err)
					}
				})
			}
		})
	}
}

func TestPostgreSQLPluginJobsConcurrentAdmission(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Now().UTC()
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("b", 64), Kind: "task.execute.v1", Key: "same-request", ActorID: 1, Payload: json.RawMessage(`{}`)}
	type result struct {
		job pluginjobs.Job
		err error
	}
	runConcurrent := func(inputs []pluginjobs.Input) []result {
		start := make(chan struct{})
		results := make(chan result, len(inputs))
		var wg sync.WaitGroup
		for _, input := range inputs {
			wg.Go(func() {
				<-start
				job, err := r.Enqueue(ctx, input, now)
				results <- result{job, err}
			})
		}
		close(start)
		wg.Wait()
		close(results)
		var out []result
		for item := range results {
			out = append(out, item)
		}
		return out
	}
	inputs := make([]pluginjobs.Input, 16)
	for i := range inputs {
		inputs[i] = in
	}
	var id string
	for _, result := range runConcurrent(inputs) {
		if result.err != nil || (id != "" && result.job.ID != id) {
			t.Fatal("concurrent replay diverged", result.err)
		}
		id = result.job.ID
	}
	page, err := r.List(ctx, pluginjobs.Query{Plugin: in.Plugin})
	if err != nil || page.Total != 1 {
		t.Fatal("duplicate task rows", page.Total, err)
	}
	// Fill all but the last per-plugin slot with valid, distinct task records.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	for i := 1; i < pluginjobs.MaxPluginJobs-1; i++ {
		q, err := taskQueue(nil)
		if err != nil {
			t.Fatal(err)
		}
		seed := in
		seed.Key = fmt.Sprintf("seed-%d", i)
		job, err := q.Enqueue(ctx, seed, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := saveTask(ctx, tx, job); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for i := range inputs {
		inputs[i].Key = fmt.Sprintf("last-slot-%d", i)
	}
	accepted := 0
	for _, result := range runConcurrent(inputs) {
		if result.err == nil {
			accepted++
		} else if !errors.Is(result.err, pluginjobs.ErrCapacity) || result.job.ID != "" {
			t.Fatal("unexpected capacity result", result.err)
		}
	}
	page, err = r.List(ctx, pluginjobs.Query{Plugin: in.Plugin})
	if err != nil || accepted != 1 || page.Total != pluginjobs.MaxPluginJobs {
		t.Fatal("concurrent admissions exceeded quota", accepted, page.Total, err)
	}
	if job, err := r.Enqueue(ctx, in, now); err != nil || job.ID != id {
		t.Fatal("full queue rejected original replay", err)
	}
	in.Plugin = "other-reader"
	if _, err := r.Enqueue(ctx, in, now); err != nil {
		t.Fatal("per-plugin quota blocked another plugin", err)
	}
}

func TestPostgreSQLPluginJobsRestoreRollsBackOnInvalidFanout(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Now().UTC()
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("c", 64), Kind: "task.execute.v1", Key: "running", ActorID: 1, Payload: json.RawMessage(`{}`)}
	job, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := r.Claim(ctx, now)
	if err != nil || !ok || claim.ID != job.ID {
		t.Fatal("claim", ok, err)
	}
	in.Key = "succeeded"
	if _, err := r.Enqueue(ctx, in, now); err != nil {
		t.Fatal(err)
	}
	success, ok, err := r.Claim(ctx, now)
	if err != nil || !ok {
		t.Fatal("success claim", ok, err)
	}
	if err := r.Finish(ctx, success, "", now); err != nil {
		t.Fatal(err)
	}
	event := pluginjobs.Event{Key: "restore:1", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	receipt, err := r.FreezeFanout(ctx, event, []pluginjobs.Subscription{{Plugin: in.Plugin, Epoch: in.Epoch, Since: now, ActorID: in.ActorID}})
	if err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, claim.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	invalid := receipt
	invalid.Event.ObjectID = 0
	raw, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE np_plugin_fanouts SET data=$2 WHERE event_key=$1`, event.Key, raw); err != nil {
		t.Fatal(err)
	}
	if err := r.CancelRestored(ctx, now.Add(time.Second)); !errors.Is(err, pluginjobs.ErrInvalid) {
		t.Fatal("invalid fanout accepted", err)
	}
	var after []byte
	if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, claim.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed restore partially cancelled tasks", err)
	}
	if err := r.CheckClaim(ctx, claim, now); err != nil {
		t.Fatal("failed restore invalidated live claim", err)
	}
	raw, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE np_plugin_fanouts SET data=$2 WHERE event_key=$1`, event.Key, raw); err != nil {
		t.Fatal(err)
	}
	if err := r.CancelRestored(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	page, err := r.List(ctx, pluginjobs.Query{Plugin: in.Plugin})
	if err != nil || page.Total != 2 {
		t.Fatal("restored task count", page.Total, err)
	}
	for _, item := range page.Items {
		want := "cancelled"
		if item.ID == success.ID {
			want = "succeeded"
		}
		if item.Status != want {
			t.Fatalf("restore status %s: %s, want %s", item.ID, item.Status, want)
		}
	}
	if err := r.CheckClaim(ctx, claim, now.Add(time.Second)); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("successful restore did not fence claim", err)
	}
	if err := pluginjobs.DeliverFrozen(ctx, r, r, event, nil, now.Add(time.Second)); err != nil {
		t.Fatal("cancelled fanout replay", err)
	}
	page, err = r.List(ctx, pluginjobs.Query{Plugin: in.Plugin})
	if err != nil || page.Total != 2 {
		t.Fatal("restored fanout resumed enqueue", page.Total, err)
	}
}
