package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/plugindispatch"
)

const webhookLoadRounds = 6

func preopenWebhookLoadConnections(t *testing.T, ctx context.Context, pool *pgxpool.Pool, count int) {
	t.Helper()
	var held []*pgxpool.Conn
	defer func() {
		for _, conn := range held {
			conn.Release()
		}
	}()
	for range count {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
		if _, err := conn.Exec(ctx, `SELECT 1`); err != nil {
			t.Fatal(err)
		}
	}
}

func seedWebhookLoad(t *testing.T, f *webhookFixture) (int, int, []string) {
	return seedWebhookLoadReviews(t, f, webhookLoadRounds)
}

func seedWebhookLoadReviews(t *testing.T, f *webhookFixture, reviewCount int) (int, int, []string) {
	t.Helper()
	accepted := f.accept(t, f.req.Slot)
	var raw []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, accepted.OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var original plugindispatch.Intent
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	count := plugindispatch.MaxTotalBytes / plugindispatch.BudgetBytes(original)
	rows := make([][]any, 0, count-1)
	reviews := []string{}
	for n := 1; n < count; n++ {
		i := original
		i.JobID = fmt.Sprintf("%064x", n)
		i.ID, _ = plugindispatch.OperationID(i.JobID, i.Slot)
		i.State, i.Version = "cancelled", 2
		i.Outcome = plugindispatch.Outcome{Code: "maintenance"}
		if n <= reviewCount {
			i.State, i.Version = "uncertain", 3
			i.AttemptToken = strings.Repeat("d", 64)
			i.AttemptAt, i.ResultAt = i.CreatedAt, i.CreatedAt
			i.AttemptUntil = i.AttemptAt.Add(plugindispatch.AttemptDuration)
			reviews = append(reviews, i.ID)
		}
		if err := plugindispatch.Validate(i); err != nil {
			t.Fatal("invalid synthetic retained row", err)
		}
		data, _ := json.Marshal(i)
		rows = append(rows, []any{i.ID, i.JobID, i.Slot, i.Version, i.State, data})
	}
	if _, err := f.pool.CopyFrom(f.ctx, pgx.Identifier{"np_plugin_webhook_intents"}, []string{"id", "job_id", "slot", "version", "state", "data"}, pgx.CopyFromRows(rows)); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `ANALYZE np_plugin_webhook_intents`)
	return count, count * plugindispatch.BudgetBytes(original), reviews
}

func webhookHeap() map[string]uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return map[string]uint64{"heap_alloc": m.HeapAlloc, "heap_inuse": m.HeapInuse, "heap_sys": m.HeapSys, "total_alloc": m.TotalAlloc, "mallocs": m.Mallocs, "num_gc": uint64(m.NumGC)}
}

// This is a local contention and allocation measurement, not a portable latency
// assertion. Two repository objects share a pool, not two deployed processes.
func TestPostgreSQLWebhookLoadMeasurements(t *testing.T) {
	if os.Getenv("NEWPRESS_WEBHOOK_LOAD") != "1" {
		t.Skip("opt-in Webhook concurrent load measurements")
	}
	for _, body := range []int{47, 16011} {
		t.Run(fmt.Sprintf("body-%d", body), func(t *testing.T) {
			f := newWebhookFixture(t)
			f.req.Body = json.RawMessage(`{"data":"` + strings.Repeat("x", body-11) + `"}`)
			count, reserved, reviews := seedWebhookLoad(t, f)
			poolMinimum := int32(1)
			var startupMillis float64
			if value := os.Getenv("NEWPRESS_WEBHOOK_LOAD_POOL_MIN"); value != "" {
				n, err := strconv.ParseInt(value, 10, 32)
				if err != nil || n < 1 || n > int64(f.pool.Config().MaxConns) {
					t.Fatal("invalid opt-in pool minimum", value, err)
				}
				// Exercise the production opener, including its ready-before-return
				// check. The disposable runner supplies a URI; preserve its schema.
				u, err := url.Parse(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
				if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
					t.Fatal("pool measurement requires a PostgreSQL URI")
				}
				query := u.Query()
				query.Set("search_path", f.pool.Config().ConnConfig.RuntimeParams["search_path"])
				u.RawQuery = query.Encode()
				poolMinimum = int32(n)
				began := time.Now()
				pool, err := OpenWithPoolLimits(f.ctx, u.String(), poolMinimum, f.pool.Config().MaxConns)
				startupMillis = float64(time.Since(began).Microseconds()) / 1000
				if err != nil {
					t.Fatal("production pool startup", err)
				}
				t.Cleanup(pool.Close)
				// Fixture setup completed on the old pool. All measured stores,
				// review writes and observers now share this fresh production pool.
				f.pool.Close()
				f.pool = pool
				f.webhooks = NewPluginWebhookRepository(pool, true)
			}
			stores := []*PluginWebhookRepository{f.webhooks, NewPluginWebhookRepository(f.pool, true)}
			preopened := 0
			if os.Getenv("NEWPRESS_WEBHOOK_LOAD_PREOPEN") == "1" {
				preopened = 6 // Five workers plus the observer; not production policy.
				preopenWebhookLoadConnections(t, f.ctx, f.pool, preopened)
			}
			runtime.GC()
			before := webhookHeap()
			for _, store := range stores {
				if err := store.Ready(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			runtime.GC()
			warm := webhookHeap()
			type sample struct {
				Operation string  `json:"operation"`
				Instance  int     `json:"repository"`
				Millis    float64 `json:"milliseconds"`
				Error     string  `json:"error,omitempty"`
				Deadline  bool    `json:"deadline_exceeded,omitempty"`
			}
			results := make(chan sample, 5*webhookLoadRounds)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for worker, op := range []string{"replay-250ms", "replay-250ms", "list", "idle-claim", "review"} {
				wg.Go(func() {
					<-start
					store := stores[worker%len(stores)]
					for n := range webhookLoadRounds {
						limit := 5 * time.Second
						if op == "replay-250ms" {
							limit = 250 * time.Millisecond
						}
						ctx, cancel := context.WithTimeout(f.ctx, limit)
						began := time.Now()
						var err error
						switch op {
						case "replay-250ms":
							var got plugindispatch.Accepted
							got, err = store.Enqueue(ctx, f.req)
							id, _, _, _ := f.req.Identity()
							if err == nil && got.OperationID != id {
								t.Error("replay changed operation identity")
							}
						case "list":
							var got plugindispatch.Page
							got, err = store.List(ctx, plugindispatch.Query{Limit: 20})
							if err == nil && (got.Total != count || len(got.Items) != 20) {
								t.Error("missing retained records")
							}
						case "idle-claim":
							var found bool
							_, found, err = store.Claim(ctx)
							if found {
								t.Error("unfinished source or synthetic terminal was claimed")
							}
						case "review":
							_, err = store.Review(ctx, plugindispatch.ReviewRequest{ID: reviews[n], Version: 3, ActorID: f.user.ID, Decision: "received", Reason: "synthetic load fixture review"})
						}
						elapsed := time.Since(began)
						deadline := errors.Is(ctx.Err(), context.DeadlineExceeded)
						cancel()
						s := sample{Operation: op, Instance: worker % len(stores), Millis: float64(elapsed.Microseconds()) / 1000}
						if err != nil {
							s.Error = err.Error()
							s.Deadline = deadline
							// Authorization, account and webhook reads use distinct
							// unavailable errors. Classify a measured timeout by the
							// actual context deadline, not one layer's error text.
							if op != "replay-250ms" || !deadline {
								t.Error(op, err)
							}
						}
						results <- s
					}
				})
			}
			type observed struct {
				heap       uint64
				waiting    int
				lockSample int
			}
			stop, sampled := make(chan struct{}), make(chan observed, 1)
			go func() {
				timer := time.NewTicker(25 * time.Millisecond)
				defer timer.Stop()
				peak := warm["heap_alloc"]
				waiting, lockSamples := 0, 0
				for {
					select {
					case <-timer.C:
						peak = max(peak, webhookHeap()["heap_alloc"])
						var blocked int
						if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&blocked); err != nil {
							t.Error("lock observation", err)
						} else {
							waiting = max(waiting, blocked)
							lockSamples++
						}
					case <-stop:
						sampled <- observed{max(peak, webhookHeap()["heap_alloc"]), waiting, lockSamples}
						return
					}
				}
			}()
			poolBefore := f.pool.Stat()
			tick := time.Now()
			close(start)
			wg.Wait()
			elapsed := time.Since(tick)
			close(stop)
			observations := <-sampled
			poolAfter := f.pool.Stat()
			end := webhookHeap()
			close(results)
			samples := []sample{}
			for s := range results {
				samples = append(samples, s)
			}
			runtime.GC()
			settled := webhookHeap()
			runtime.KeepAlive(stores)
			for _, store := range stores {
				page, err := store.List(f.ctx, plugindispatch.Query{State: "reviewed_received", Limit: 20})
				if err != nil || page.Total != webhookLoadRounds {
					t.Fatal("other repository did not observe all committed reviews", page.Total, err)
				}
			}
			var actual int
			if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM np_plugin_webhook_intents`).Scan(&actual); err != nil || actual != count {
				t.Fatal("load duplicated a retained identity", actual, err)
			}
			report, _ := json.Marshal(map[string]any{"body_bytes": body, "rows": count, "reserved_bytes": reserved, "repositories": len(stores), "workers": 5,
				"samples": samples, "seconds": elapsed.Seconds(), "heap_before_warm": before, "heap_after_warm": warm, "heap_after_load": end, "heap_after_gc": settled, "sampled_peak_heap_alloc": observations.heap,
				"sampled_max_waiting_advisory_locks": observations.waiting, "lock_samples": observations.lockSample, "pool_max_connections": f.pool.Config().MaxConns,
				"pool_preopened_connections": preopened,
				"pool_configured_minimum":    poolMinimum, "pool_startup_milliseconds": startupMillis,
				"pool_empty_acquires": poolAfter.EmptyAcquireCount() - poolBefore.EmptyAcquireCount(), "pool_acquire_milliseconds": float64((poolAfter.AcquireDuration() - poolBefore.AcquireDuration()).Microseconds()) / 1000,
				"scope": "one process, one shared pool, two independent repository memos, five synchronized goroutines; 25ms Go heap/PG lock sampling adds overhead and is not RSS or a guaranteed peak; synthetic terminal/uncertain rows; real review transactions; no sender"})
			t.Log("WEBHOOK_LOAD " + string(report))
		})
	}
}
