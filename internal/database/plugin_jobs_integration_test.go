package database

import (
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

func TestPostgreSQLPluginJobs(t *testing.T) {
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
	// Claims are global to a queue, so a unique plugin name alone does not
	// isolate this test from other task rows. Use a disposable schema instead.
	schema := fmt.Sprintf("pluginjobs_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, e := pool.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
	}()
	config := pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	if err = Migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	plugin := fmt.Sprintf("tasks-%d", time.Now().UnixNano())
	r := NewPluginJobRepository(isolated)
	now := time.Now().UTC().Truncate(time.Microsecond)
	in := pluginjobs.Input{Plugin: plugin, Epoch: strings.Repeat("b", 64), Kind: "task.execute", Key: "one", Payload: json.RawMessage(`{"id":1}`)}
	original, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.Enqueue(ctx, in, now.Add(time.Second))
	if err != nil || replay.ID != original.ID || !replay.CreatedAt.Equal(now) {
		t.Fatalf("replay: %v", err)
	}
	changed := in
	changed.Payload = json.RawMessage(`{"id":2}`)
	if _, err = r.Enqueue(ctx, changed, now); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatalf("conflicting replay: %v", err)
	}
	var wg sync.WaitGroup
	claims := make(chan pluginjobs.Job, 16)
	for range 16 {
		wg.Go(func() {
			j, ok, e := r.Claim(ctx, now)
			if e != nil {
				t.Error(e)
			}
			if ok {
				claims <- j
			}
		})
	}
	wg.Wait()
	close(claims)
	var first pluginjobs.Job
	count := 0
	for j := range claims {
		first = j
		count++
	}
	if count != 1 || first.ID != original.ID {
		t.Fatalf("claim winners: %d", count)
	}
	later := now.Add(pluginjobs.LeaseDuration)
	second, ok, err := r.Claim(ctx, later)
	if err != nil || !ok || second.ID != first.ID || second.LeaseToken == first.LeaseToken {
		t.Fatalf("lease reclaim: %v %v", ok, err)
	}
	if err = r.Finish(ctx, first, "", later); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatalf("stale completion: %v", err)
	}
	if err = r.Change(ctx, plugin, second.ID, second.Version, "cancel", later); err != nil {
		t.Fatal(err)
	}
	if err = r.Finish(ctx, second, "", later); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatalf("cancel fencing: %v", err)
	}
	replay, err = NewPluginJobRepository(isolated).Enqueue(ctx, in, later)
	if err != nil || replay.Status != "cancelled" {
		t.Fatalf("reopen: %s %v", replay.Status, err)
	}
	event := pluginjobs.Event{Key: "publication:100:1", Kind: "content.published", ObjectID: 100, Version: 1, At: now}
	subs := []pluginjobs.Subscription{{Plugin: plugin, Epoch: in.Epoch, Since: now, ActorID: 1}}
	if _, found, err := r.FindFanout(ctx, event); err != nil || found {
		t.Fatal("lookup created receipt", found, err)
	}
	frozen, err := r.FreezeFanout(ctx, event, subs)
	if err != nil || len(frozen.Subscriptions) != 1 {
		t.Fatal("freeze", err)
	}
	frozen, err = NewPluginJobRepository(isolated).FreezeFanout(ctx, event, nil)
	if err != nil || len(frozen.Subscriptions) != 1 {
		t.Fatal("receipt reopen", err)
	}
	if receipt, found, err := NewPluginJobRepository(isolated).FindFanout(ctx, event); err != nil || !found || len(receipt.Subscriptions) != 1 {
		t.Fatal("lookup lost persisted receipt", found, err)
	}
	changedEvent := event
	changedEvent.Version++
	if _, _, err := r.FindFanout(ctx, changedEvent); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatal("lookup accepted changed event", err)
	}
	if _, err = r.FreezeFanout(ctx, changedEvent, nil); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatal("changed event", err)
	}
	for range 2 {
		if err = pluginjobs.DeliverFrozen(ctx, r, r, event, nil, now); err != nil {
			t.Fatal(err)
		}
	}
	if receipt, found, err := NewPluginJobRepository(isolated).FindFanout(ctx, event); err != nil || !found || !receipt.Delivered {
		t.Fatal("fanout completion lost", found, err)
	}
	if err := r.CompleteFanout(ctx, changedEvent); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatal("completion accepted changed event", err)
	}
	page, err := r.List(ctx, pluginjobs.Query{Plugin: plugin})
	if err != nil || page.Total != 2 {
		t.Fatal("duplicate fanout", page.Total, err)
	}
	claim, ok, err := r.Claim(ctx, later)
	if err != nil || !ok {
		t.Fatal("restore claim", ok, err)
	}
	if err = r.CancelRestored(ctx, later.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = r.Finish(ctx, claim, "", later.Add(2*time.Second)); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatal("restore did not fence claim", err)
	}
	if receipt, found, err := r.FindFanout(ctx, event); err != nil || !found || !receipt.Cancelled {
		t.Fatal("restore receipt", found, err)
	}
	if err = pluginjobs.DeliverFrozen(ctx, r, r, event, nil, later); err != nil {
		t.Fatal(err)
	}
	page, err = r.List(ctx, pluginjobs.Query{Plugin: plugin})
	if err != nil || page.Total != 2 {
		t.Fatal("restore replay", err)
	}
	for _, item := range page.Items {
		if item.Status != "cancelled" {
			t.Fatal("restored task resumed", item.Status)
		}
	}
}
