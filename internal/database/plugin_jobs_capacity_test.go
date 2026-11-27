package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestPostgreSQLPluginTaskCapacityKeepsWorkExecutable(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schema := fmt.Sprintf("plugin_capacity_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config := pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	if err := Migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	repository := NewPluginJobRepository(isolated)
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	in := pluginjobs.Input{Plugin: "capacity-task", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "near-full", Payload: json.RawMessage(`{}`)}
	makeJob := func(input pluginjobs.Input) pluginjobs.Job {
		queue, err := taskQueue(nil)
		if err != nil {
			t.Fatal(err)
		}
		job, err := queue.Enqueue(ctx, input, now)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	candidate := makeJob(in)
	candidateRaw, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	// Seed legal completed receipts in one transaction, leaving exactly one
	// byte after the candidate. No rows can be claimed except the new task.
	target := pluginjobs.MaxStateBytes - len(candidateRaw) - 1
	size := 0
	tx, err := isolated.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var fillerInput pluginjobs.Input
	var filler pluginjobs.Job
	for index := 0; size < target; index++ {
		item := in
		item.Plugin = fmt.Sprintf("capacity-fill-%d", index/900)
		item.Key = fmt.Sprintf("filler-%d", index)
		item.Payload = json.RawMessage(`"` + strings.Repeat("x", pluginjobs.MaxPayloadBytes-2) + `"`)
		job := makeJob(item)
		job.Status = "succeeded"
		raw, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		if size+len(raw) > target {
			remaining := target - size
			if remaining < len(raw)-(pluginjobs.MaxPayloadBytes-2) {
				t.Fatal("fixture remainder is too small for a valid task row", remaining)
			}
			item.Payload = json.RawMessage(`"` + strings.Repeat("x", pluginjobs.MaxPayloadBytes-2-(size+len(raw)-target)) + `"`)
			job = makeJob(item)
			job.Status = "succeeded"
			raw, err = json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := saveTask(ctx, tx, job); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			fillerInput, filler = item, job
		}
		size += len(raw)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := repository.Enqueue(ctx, in, now); !errors.Is(err, pluginjobs.ErrCapacity) || got.ID != "" {
		t.Errorf("SQL accepted a task without execution metadata space: id=%q err=%v", got.ID, err)
		if err == nil {
			_, ok, claimErr := repository.Claim(ctx, now)
			var storedBytes int64
			sizeErr := isolated.QueryRow(ctx, `SELECT sum(octet_length(data)) FROM np_plugin_jobs`).Scan(&storedBytes)
			t.Logf("claim after admission: claimed=%v err=%v bytes=%d limit=%d sizeErr=%v", ok, claimErr, storedBytes, pluginjobs.MaxStateBytes, sizeErr)
		}
		t.FailNow()
	}
	page, err := repository.List(ctx, pluginjobs.Query{Plugin: in.Plugin})
	if err != nil || page.Total != 0 {
		t.Fatal("rejected admission left a task row", err)
	}
	replay, err := repository.Enqueue(ctx, fillerInput, now)
	if err != nil || replay.ID != filler.ID || replay.Status != "succeeded" {
		t.Fatal("capacity blocked an existing receipt", err)
	}
	// Free sufficient space in the fixture, then exercise the ordinary API.
	if _, err := isolated.Exec(ctx, `DELETE FROM np_plugin_jobs WHERE id=$1`, filler.ID); err != nil {
		t.Fatal(err)
	}
	original, err := repository.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal("available capacity was rejected", err)
	}
	claim, ok, err := repository.Claim(ctx, now.Add(123456789*time.Nanosecond))
	if err != nil || !ok || claim.ID != original.ID {
		t.Fatal("admitted task could not acquire lease", err)
	}
	if err := repository.Finish(ctx, claim, "execution_failed", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	claim, ok, err = repository.Claim(ctx, now.Add(4*time.Second))
	if err != nil || !ok || claim.Attempts != 2 {
		t.Fatal("retry could not acquire lease", err)
	}
	if err := repository.Finish(ctx, claim, "", now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	replay, err = NewPluginJobRepository(isolated).Enqueue(ctx, in, now.Add(6*time.Second))
	if err != nil || replay.ID != original.ID || replay.Status != "succeeded" {
		t.Fatal("completion did not survive SQL reload", err)
	}
	var finalBytes int64
	if err := isolated.QueryRow(ctx, `SELECT sum(octet_length(data)) FROM np_plugin_jobs`).Scan(&finalBytes); err != nil || finalBytes > pluginjobs.MaxStateBytes {
		t.Fatal("execution exceeded total task storage limit", finalBytes, err)
	}
}
