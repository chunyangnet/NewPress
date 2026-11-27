package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/plugindispatch"
)

func TestPostgreSQLWebhookMemoRechecksBytesIndexesAndOtherRows(t *testing.T) {
	for _, mode := range []string{"invalid-json", "unknown-field", "duplicate-field", "changed-body", "id", "job", "slot", "version", "state", "other-row"} {
		t.Run(mode, func(t *testing.T) {
			f, id, raw := webhookMaintenanceState(t, "uncertain")
			if err := f.webhooks.Ready(f.ctx); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "invalid-json":
				f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$1`, []byte(`{`))
			case "unknown-field":
				f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$1`, bytes.Replace(raw, []byte(`{"schema":`), []byte(`{"unknown":true,"schema":`), 1))
			case "duplicate-field":
				f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$1`, bytes.Replace(raw, []byte(`{"schema":`), []byte(`{"schema":1,"schema":`), 1))
			case "changed-body":
				f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$1`, bytes.Replace(raw, []byte("secret body"), []byte("edited body"), 1))
			case "id":
				f.exec(t, `UPDATE np_plugin_webhook_intents SET id=$1`, strings.Repeat("0", 64))
			case "job":
				f.exec(t, `UPDATE np_plugin_webhook_intents SET job_id=$1`, strings.Repeat("0", 64))
			case "slot":
				f.exec(t, `UPDATE np_plugin_webhook_intents SET slot='different'`)
			case "version":
				f.exec(t, `UPDATE np_plugin_webhook_intents SET version=4`)
			case "state":
				f.exec(t, `UPDATE np_plugin_webhook_intents SET state='http_confirmed'`)
			case "other-row":
				f.exec(t, `INSERT INTO np_plugin_webhook_intents(id,job_id,slot,version,state,data) VALUES($1,$1,'unrelated',1,'pending',$2)`, strings.Repeat("0", 64), []byte(`{`))
			}
			if err := f.webhooks.Ready(f.ctx); !errors.Is(err, plugindispatch.ErrCorrupt) {
				t.Fatal("readiness accepted changed bytes/index", err)
			}
			if _, err := f.webhooks.List(f.ctx, plugindispatch.Query{JobID: f.req.Authorization.Claim.ID}); !errors.Is(err, plugindispatch.ErrCorrupt) {
				t.Fatal("filtered read skipped corrupt unrelated evidence", err)
			}
			if _, ok, err := f.webhooks.Claim(f.ctx); ok || !errors.Is(err, plugindispatch.ErrCorrupt) {
				t.Fatal("cached claim bypassed corruption", err)
			}
			if _, err := f.webhooks.Review(f.ctx, plugindispatch.ReviewRequest{ID: id, Version: 3, ActorID: f.user.ID, Decision: "received", Reason: "current database must remain authoritative"}); !errors.Is(err, plugindispatch.ErrCorrupt) {
				t.Fatal("cached review bypassed corruption", err)
			}
		})
	}
}

func TestPostgreSQLWebhookMemoOldReaderAndCallerMutation(t *testing.T) {
	f, id, raw := webhookMaintenanceState(t, "reviewed_received")
	var original plugindispatch.Intent
	if json.Unmarshal(raw, &original) != nil {
		t.Fatal("invalid original")
	}
	old, err := f.pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Rollback(context.Background())
	first, _, _, err := f.webhooks.selectIntents(f.ctx, old, nil)
	if err != nil || len(first) != 1 {
		t.Fatal(err)
	}
	first[0].Body[0] = '!'
	first[0].Review.Reason = "caller edited its own copy"
	changed := cloneWebhook(original)
	changed.Review.Reason = "verified database change without incrementing indexed version"
	updated, _ := json.Marshal(changed)
	f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$2 WHERE id=$1`, id, updated)
	check := func(expected string) {
		t.Helper()
		page, err := f.webhooks.List(f.ctx, plugindispatch.Query{})
		if err != nil || len(page.Items) != 1 || page.Items[0].Review.Reason != expected {
			t.Fatal("memo obscured actual snapshot", page, err)
		}
	}
	check(changed.Review.Reason)
	// The old repeatable-read transaction finishes after a newer one and
	// republishes its parsing hints. It must not roll back later visibility.
	stale, _, _, err := f.webhooks.selectIntents(f.ctx, old, nil)
	if err != nil || len(stale) != 1 || stale[0].Review.Reason != original.Review.Reason || !bytes.Equal(stale[0].Body, original.Body) {
		t.Fatal("caller modified cached data or read crossed snapshots", err)
	}
	if err = old.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	check(changed.Review.Reason)
	// Concurrent reads share only immutable parsing results, never mutable
	// bodies/review objects or a transaction's visibility and permissions.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 4 {
				page, err := f.webhooks.List(f.ctx, plugindispatch.Query{})
				if err != nil || len(page.Items) != 1 || page.Items[0].Review.Reason != changed.Review.Reason {
					t.Error("concurrent snapshot", err)
					return
				}
				page.Items[0].Review.Reason = "private caller mutation"
			}
		})
	}
	wg.Wait()
	check(changed.Review.Reason)
	ctx, cancel := context.WithDeadline(f.ctx, time.Now().Add(-time.Second))
	defer cancel()
	if _, err = f.webhooks.List(ctx, plugindispatch.Query{}); err == nil {
		t.Fatal("memo served data without a database read")
	}
}

func TestPostgreSQLWebhookMemoRecomputesSlotBudget(t *testing.T) {
	f := newWebhookFixture(t)
	for n := range plugindispatch.MaxSlotsPerTask {
		f.accept(t, fmt.Sprintf("slot%d", n))
	}
	if err := f.webhooks.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var extra plugindispatch.Intent
	if err := json.Unmarshal(raw, &extra); err != nil {
		t.Fatal(err)
	}
	extra.Slot = "overflow"
	extra.ID, _ = plugindispatch.OperationID(extra.JobID, extra.Slot)
	if err := plugindispatch.Validate(extra); err != nil {
		t.Fatal("overflow fixture must be individually valid", err)
	}
	raw, _ = json.Marshal(extra)
	f.exec(t, `INSERT INTO np_plugin_webhook_intents(id,job_id,slot,version,state,data) VALUES($1,$2,$3,$4,$5,$6)`, extra.ID, extra.JobID, extra.Slot, extra.Version, extra.State, raw)
	if err := f.webhooks.Ready(f.ctx); !errors.Is(err, plugindispatch.ErrCorrupt) {
		t.Fatal("warm rows escaped total slot validation", err)
	}
	f.exec(t, `DELETE FROM np_plugin_webhook_intents WHERE id=$1`, extra.ID)
	if err := f.webhooks.Ready(f.ctx); err != nil {
		t.Fatal("deleted overflow row remained visible", err)
	}
}

func TestPostgreSQLWebhookMemoDoesNotExposeRolledBackRows(t *testing.T) {
	f, id, raw := webhookMaintenanceState(t, "reviewed_received")
	var original plugindispatch.Intent
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	changed := cloneWebhook(original)
	changed.Review.Reason = "uncommitted review visible only inside this transaction"
	updated, _ := json.Marshal(changed)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(f.ctx, `UPDATE np_plugin_webhook_intents SET data=$2 WHERE id=$1`, id, updated); err != nil {
		t.Fatal(err)
	}
	inside, _, _, err := f.webhooks.selectIntents(f.ctx, tx, nil)
	if err != nil || len(inside) != 1 || inside[0].Review.Reason != changed.Review.Reason {
		t.Fatal("transaction did not see its own update", err)
	}
	if err = tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	page, err := f.webhooks.List(f.ctx, plugindispatch.Query{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Review.Reason != original.Review.Reason {
		t.Fatal("memo leaked rolled-back data", err)
	}
}

func TestPostgreSQLWebhookSelectionRejectsUnselectedRows(t *testing.T) {
	for _, op := range []string{"enqueue", "finish"} {
		t.Run(op, func(t *testing.T) {
			f := newWebhookFixture(t)
			f.accept(t, f.req.Slot)
			var attempt plugindispatch.Intent
			if op == "finish" {
				f.success(t)
				attempt = f.claim(t)
			}
			if err := f.webhooks.Ready(f.ctx); err != nil {
				t.Fatal(err)
			}
			f.exec(t, `INSERT INTO np_plugin_webhook_intents(id,job_id,slot,version,state,data) VALUES($1,$1,'unrelated',1,'pending',$2)`, strings.Repeat("f", 64), []byte(`{`))
			var err error
			if op == "enqueue" {
				_, err = f.webhooks.Enqueue(f.ctx, f.req)
			} else {
				err = f.webhooks.Finish(f.ctx, attempt, plugindispatch.Outcome{Code: "transport_unconfirmed"})
			}
			if !errors.Is(err, plugindispatch.ErrCorrupt) {
				t.Fatal("selected operation ignored later corrupt row", err)
			}
		})
	}
}

func TestPostgreSQLWebhookSelectionOwnsBodiesAcrossDriverReads(t *testing.T) {
	f := newWebhookFixture(t)
	want := map[string][]byte{}
	for n := range 12 {
		r := f.req
		r.Slot = fmt.Sprintf("body%d", n)
		r.Body = json.RawMessage(`{"data":"` + strings.Repeat(string(rune('a'+n)), 15000+n) + `"}`)
		a, err := f.webhooks.Enqueue(f.ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		want[a.OperationID] = bytes.Clone(r.Body)
	}
	f.success(t)
	for range len(want) {
		i := f.claim(t)
		if !bytes.Equal(i.Body, want[i.ID]) {
			t.Fatal("driver buffer reuse changed selected body")
		}
		original := bytes.Clone(i.Body)
		i.Body[0] = '!'
		if err := f.webhooks.Ready(f.ctx); err != nil {
			t.Fatal("returned body mutated memo", err)
		}
		i.Body = original
		if err := f.webhooks.Finish(f.ctx, i, plugindispatch.Outcome{Code: "transport_unconfirmed"}); err != nil {
			t.Fatal(err)
		}
		delete(want, i.ID)
	}
	f.assertNoClaim(t)
	page, err := f.webhooks.List(f.ctx, plugindispatch.Query{State: "uncertain", Offset: 2, Limit: 4})
	if err != nil || page.Total != 12 || len(page.Items) != 4 {
		t.Fatal("summary projection changed filtering or pagination", page, err)
	}
	page, err = f.webhooks.List(f.ctx, plugindispatch.Query{State: "pending"})
	if err != nil || page.Total != 0 || page.Items == nil || len(page.Items) != 0 {
		t.Fatal("empty summary collection must remain an array", page, err)
	}
}
