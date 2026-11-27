package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/plugindispatch"
)

type webhookSoakInput struct {
	Schema    string   `json:"schema"`
	Directory string   `json:"directory"`
	Worker    int      `json:"worker"`
	Body      int      `json:"body_bytes"`
	Rows      int      `json:"rows"`
	Actor     int64    `json:"actor"`
	Reviews   []string `json:"reviews"`
}

type webhookSoakSample struct {
	Operation string  `json:"operation"`
	Millis    float64 `json:"milliseconds"`
}

type webhookSoakReply struct {
	PID        int                 `json:"pid"`
	Body       int                 `json:"body_bytes"`
	Worker     int                 `json:"worker"`
	Iterations int                 `json:"iterations"`
	Seconds    float64             `json:"seconds"`
	Warm       map[string]uint64   `json:"heap_after_warm"`
	End        map[string]uint64   `json:"heap_after_load"`
	Settled    map[string]uint64   `json:"heap_after_gc"`
	Peak       uint64              `json:"sampled_peak_heap_alloc"`
	Samples    []webhookSoakSample `json:"samples"`
}

func webhookSoakWrite(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	// Publish a complete file so another process never reads a partial JSON.
	if err := os.WriteFile(path+".tmp", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

func webhookSoakRead(t *testing.T, ctx context.Context, path string, value any) {
	t.Helper()
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			if err := json.Unmarshal(raw, value); err != nil {
				t.Fatal(err)
			}
			return
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("subprocess synchronization", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestPluginWebhookSoakChild(t *testing.T) {
	if os.Getenv("NEWPRESS_WEBHOOK_SOAK_CHILD") != "1" {
		t.Skip("opt-in independent process load entry")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Second)
	defer cancel()
	var input webhookSoakInput
	webhookSoakRead(t, ctx, os.Getenv("NEWPRESS_WEBHOOK_SOAK_INPUT"), &input)
	config, err := pgxpool.ParseConfig(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("temporary database configuration")
	}
	config.ConnConfig.RuntimeParams["search_path"] = input.Schema
	config.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("temporary database unavailable")
	}
	defer pool.Close()
	preopenWebhookLoadConnections(t, ctx, pool, 3)
	store := NewPluginWebhookRepository(pool, true)
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	reply := webhookSoakReply{PID: os.Getpid(), Body: input.Body, Worker: input.Worker, Warm: webhookHeap()}
	reply.Peak = reply.Warm["heap_alloc"]
	webhookSoakWrite(t, filepath.Join(input.Directory, fmt.Sprintf("ready-%d.json", input.Worker)), reply.PID)
	var start time.Time
	webhookSoakRead(t, ctx, filepath.Join(input.Directory, "start.json"), &start)
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-time.After(time.Until(start)):
	}
	began := time.Now()
	finish := start.Add(45 * time.Second)
	for time.Now().Before(finish) {
		iteration := time.Now()
		limited, stop := context.WithTimeout(ctx, 5*time.Second)
		tick := time.Now()
		page, err := store.List(limited, plugindispatch.Query{Limit: 20})
		reply.Samples = append(reply.Samples, webhookSoakSample{"list", float64(time.Since(tick).Microseconds()) / 1000})
		stop()
		if err != nil || page.Total != input.Rows || len(page.Items) != 20 {
			t.Fatal("changed retained collection", page.Total, err)
		}
		if reply.Iterations >= len(input.Reviews) {
			t.Fatal("review fixture exhausted before timed interval")
		}
		limited, stop = context.WithTimeout(ctx, 5*time.Second)
		tick = time.Now()
		got, err := store.Review(limited, plugindispatch.ReviewRequest{ID: input.Reviews[reply.Iterations], Version: 3, ActorID: input.Actor, Decision: "received", Reason: "synthetic independent process measurement"})
		reply.Samples = append(reply.Samples, webhookSoakSample{"review", float64(time.Since(tick).Microseconds()) / 1000})
		stop()
		if err != nil || got.State != "reviewed_received" {
			t.Fatal("review transaction", got.State, err)
		}
		reply.Iterations++
		reply.Peak = max(reply.Peak, webhookHeap()["heap_alloc"])
		// Bound fixture consumption while keeping commits throughout the run.
		// This is deliberately paced traffic, not saturated throughput.
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(max(0, 250*time.Millisecond-time.Since(iteration))):
		}
	}
	reply.Seconds = time.Since(began).Seconds()
	reply.End = webhookHeap()
	runtime.GC()
	reply.Settled = webhookHeap()
	runtime.KeepAlive(store)
	webhookSoakWrite(t, filepath.Join(input.Directory, fmt.Sprintf("result-%d.json", input.Worker)), reply)
	// Leave the post-GC process alive briefly for the external OS sampler.
	time.Sleep(300 * time.Millisecond)
}

// Two independent processes and pools, real committed reviews, synthetic
// retained history, no sender. This is a 45-second paced observation per body
// size, not a full HTTP deployment or long-term/saturated capacity acceptance.
func TestPostgreSQLWebhookSoakMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_WEBHOOK_SOAK") != "1" {
		t.Skip("opt-in independent process Webhook load measurements")
	}
	for _, body := range []int{47, 16011} {
		t.Run(fmt.Sprintf("body-%d", body), func(t *testing.T) {
			f := newWebhookFixture(t)
			f.req.Body = json.RawMessage(`{"data":"` + strings.Repeat("x", body-11) + `"}`)
			count, reserved, reviews := seedWebhookLoadReviews(t, f, 512)
			directory := t.TempDir()
			var commands []*exec.Cmd
			var outputs []*bytes.Buffer
			var pids []int
			for worker := range 2 {
				input := webhookSoakInput{Schema: f.pool.Config().ConnConfig.RuntimeParams["search_path"], Directory: directory, Worker: worker, Body: body, Rows: count, Actor: f.user.ID}
				for n := worker; n < len(reviews); n += 2 {
					input.Reviews = append(input.Reviews, reviews[n])
				}
				path := filepath.Join(directory, fmt.Sprintf("input-%d.json", worker))
				webhookSoakWrite(t, path, input)
				cmd := exec.CommandContext(f.ctx, os.Args[0], "-test.run=^TestPluginWebhookSoakChild$", "-test.v")
				cmd.Env = append(os.Environ(), "NEWPRESS_WEBHOOK_SOAK_CHILD=1", "NEWPRESS_WEBHOOK_SOAK_INPUT="+path)
				out := &bytes.Buffer{}
				cmd.Stdout, cmd.Stderr = out, out
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				commands, outputs = append(commands, cmd), append(outputs, out)
				// Kill and reap only on an early parent failure; successful Wait
				// below sets ProcessState and has already released the process.
				t.Cleanup(func() {
					if cmd.ProcessState == nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				})
				pids = append(pids, cmd.Process.Pid)
			}
			ready, stop := context.WithTimeout(f.ctx, 25*time.Second)
			defer stop()
			for worker := range 2 {
				var pid int
				webhookSoakRead(t, ready, filepath.Join(directory, fmt.Sprintf("ready-%d.json", worker)), &pid)
				if pid != pids[worker] || pid == os.Getpid() || (worker == 1 && pid == pids[0]) {
					t.Fatal("not two independent processes", pid, pids)
				}
			}
			start := time.Now().Add(300 * time.Millisecond)
			if monitor := os.Getenv("NEWPRESS_WEBHOOK_SOAK_MONITOR"); monitor != "" {
				webhookSoakWrite(t, filepath.Join(monitor, fmt.Sprintf("body-%d.json", body)), map[string]any{"body_bytes": body, "pids": pids, "start": start, "seconds": 45})
			}
			webhookSoakWrite(t, filepath.Join(directory, "start.json"), start)
			var replies []webhookSoakReply
			completed := 0
			for worker, cmd := range commands {
				if err := cmd.Wait(); err != nil {
					t.Fatalf("load child failed: %v: %s", err, outputs[worker].String())
				}
				var reply webhookSoakReply
				webhookSoakRead(t, f.ctx, filepath.Join(directory, fmt.Sprintf("result-%d.json", worker)), &reply)
				if reply.PID != pids[worker] || reply.Iterations < 20 || len(reply.Samples) != reply.Iterations*2 {
					t.Fatal("incomplete child run", reply.PID, reply.Iterations)
				}
				completed += reply.Iterations
				replies = append(replies, reply)
			}
			cold := NewPluginWebhookRepository(f.pool, true)
			page, err := cold.List(f.ctx, plugindispatch.Query{State: "reviewed_received", Limit: 20})
			if err != nil || page.Total != completed {
				t.Fatal("cold reader missed committed reviews", page.Total, completed, err)
			}
			var rows, audits int
			if err := f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM np_plugin_webhook_intents),(SELECT count(*) FROM np_audit_events WHERE action='plugin.webhook.reviewed')`).Scan(&rows, &audits); err != nil || rows != count || audits != completed {
				t.Fatal("load changed retained identities or audit count", rows, audits, err)
			}
			raw, _ := json.Marshal(map[string]any{"body_bytes": body, "rows": count, "reserved_bytes": reserved, "completed_reviews": completed, "processes": replies,
				"scope": "two independent child processes, one preopened max-3 pool and memo each; 45 seconds of paced list/review operations; 512 synthetic uncertain rows, one real pending source and remaining synthetic cancelled; no sender, replay, HTTP sites or saturated/long-term acceptance; HeapAlloc sampled per iteration, forced GC before/after only"})
			t.Log("WEBHOOK_SOAK " + string(raw))
		})
	}
}
