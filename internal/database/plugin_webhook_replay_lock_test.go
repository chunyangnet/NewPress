package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/pluginjobs"
)

func waitWebhookAdmissionLocks(t *testing.T, f *webhookFixture, mode string, granted bool, minimum int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 4*time.Second)
	defer cancel()
	for {
		var count int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND mode=$1 AND granted=$2 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`, mode, granted).Scan(&count); err != nil {
			t.Fatal("admission lock observation", mode, count, err)
		}
		if count >= minimum {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("admission lock not reached", mode, count, ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestPostgreSQLWebhookReplayOverlapsAndReleasesQueuedWriter(t *testing.T) {
	f := newWebhookFixture(t)
	want := f.accept(t, f.req.Slot)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(f.ctx, `SELECT id FROM np_users WHERE id=$1 FOR UPDATE`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		accepted plugindispatch.Accepted
		err      error
	}
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	done := make(chan result, 2)
	for range 2 {
		go func() {
			a, err := f.webhooks.Enqueue(ctx, f.req)
			done <- result{a, err}
		}()
	}
	// Both transactions hold shared admission while waiting on the account.
	// Merely accepting alongside an unrelated shared holder is insufficient.
	waitWebhookAdmissionLocks(t, f, "ShareLock", true, 2)
	writer := make(chan result, 1)
	r := f.req
	r.Slot = "queued-writer"
	go func() {
		a, err := f.webhooks.Enqueue(ctx, r)
		writer <- result{a, err}
	}()
	waitWebhookAdmissionLocks(t, f, "ExclusiveLock", false, 1)
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got := <-done
		if got.err != nil || got.accepted != want {
			t.Fatal("overlapping confirmation", got)
		}
	}
	if got := <-writer; got.err != nil || got.accepted.OperationID == want.OperationID || got.accepted.OperationID == "" {
		t.Fatal("queued writer did not resume", got)
	}
	var rows, audits int
	if err := f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM np_plugin_webhook_intents),(SELECT count(*) FROM np_audit_events WHERE action='plugin.webhook.accepted')`).Scan(&rows, &audits); err != nil || rows != 2 || audits != 2 {
		t.Fatal("confirmation wrote or writer duplicated", rows, audits, err)
	}
}

func TestPostgreSQLWebhookReplayMissesKeepIdentityAndSlotBudgetAtomic(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("last-slot-%v", full), func(t *testing.T) {
			f := newWebhookFixture(t)
			if full {
				for n := range plugindispatch.MaxSlotsPerTask - 1 {
					f.accept(t, fmt.Sprintf("retained-%d", n))
				}
			}
			tx, err := f.pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if err := webhookLock(f.ctx, tx); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			defer cancel()
			type result struct {
				a plugindispatch.Accepted
				e error
			}
			done := make(chan result, 2)
			for n := range 2 {
				r := f.req
				if full {
					r.Slot = fmt.Sprintf("last-%d", n)
				}
				go func() {
					a, err := f.webhooks.Enqueue(ctx, r)
					done <- result{a, err}
				}()
			}
			waitWebhookAdmissionLocks(t, f, "ExclusiveLock", false, 2)
			if err := tx.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			first, second := <-done, <-done
			want := 1
			if full {
				want = plugindispatch.MaxSlotsPerTask
				if !((first.e == nil && errors.Is(second.e, plugindispatch.ErrCapacity)) || (second.e == nil && errors.Is(first.e, plugindispatch.ErrCapacity))) {
					t.Fatal("last slot admitted twice or failed both", first, second)
				}
			} else if first.e != nil || second.e != nil || first.a != second.a || first.a.OperationID == "" {
				t.Fatal("concurrent misses did not reuse committed identity", first, second)
			}
			var rows, audits int
			if err := f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM np_plugin_webhook_intents),(SELECT count(*) FROM np_audit_events WHERE action='plugin.webhook.accepted')`).Scan(&rows, &audits); err != nil || rows != want || audits != want {
				t.Fatal("non-atomic admission", rows, audits, err)
			}
		})
	}
}

func TestPostgreSQLWebhookReplaySharedAdmissionKeepsNewWritesExclusive(t *testing.T) {
	f := newWebhookFixture(t)
	accepted := f.accept(t, f.req.Slot)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(f.ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('newpress-plugin-webhook-admission',0))`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	got, err := f.webhooks.Enqueue(ctx, f.req)
	if err != nil || got != accepted {
		t.Fatal("confirmation could not coexist with a shared admission holder", got, err)
	}
	r := f.req
	r.Body = json.RawMessage(`{"changed":true}`)
	if _, err := f.webhooks.Enqueue(ctx, r); !errors.Is(err, plugindispatch.ErrConflict) {
		t.Fatal("shared confirmation accepted a changed request", err)
	}
	r = f.req
	r.Slot = "another"
	limited, stop := context.WithTimeout(f.ctx, 100*time.Millisecond)
	defer stop()
	if _, err := f.webhooks.Enqueue(limited, r); !errors.Is(err, plugindispatch.ErrUnavailable) || !errors.Is(limited.Err(), context.DeadlineExceeded) {
		t.Fatal("new intent bypassed the shared lock or failed for another reason", err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	var rows, audits int
	if err := f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM np_plugin_webhook_intents),(SELECT count(*) FROM np_audit_events WHERE action='plugin.webhook.accepted')`).Scan(&rows, &audits); err != nil || rows != 1 || audits != 1 {
		t.Fatal("confirmation wrote an intent or audit", rows, audits, err)
	}
	if _, err := f.webhooks.Enqueue(f.ctx, r); err != nil {
		t.Fatal("new exclusive admission did not resume", err)
	}
}

func TestPostgreSQLWebhookReplayRechecksAccountAfterWaiting(t *testing.T) {
	f := newWebhookFixture(t)
	f.accept(t, f.req.Slot)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(f.ctx, `SELECT id FROM np_users WHERE id=$1 FOR UPDATE`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := f.webhooks.Enqueue(f.ctx, f.req); done <- err }()
	select {
	case err := <-done:
		t.Fatal("confirmation bypassed locked account", err)
	case <-time.After(120 * time.Millisecond):
	}
	if _, err := tx.Exec(f.ctx, `UPDATE np_users SET status='suspended' WHERE id=$1`, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, pluginjobs.ErrAuthorization) {
			t.Fatal("shared replay ignored committed revocation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("confirmation did not resume")
	}
}

func TestPostgreSQLWebhookReplayNeverReinsertsVanishedIdentity(t *testing.T) {
	f := newWebhookFixture(t)
	accepted := f.accept(t, f.req.Slot)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := webhookLock(f.ctx, tx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := f.webhooks.Enqueue(f.ctx, f.req); done <- err }()
	// Observe that the existing-ID hint has been read and the confirmation is
	// actually waiting for its shared advisory lock before deleting out of band.
	wait, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	for {
		var blocked bool
		if err := f.pool.QueryRow(wait, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND mode='ShareLock' AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&blocked); err != nil {
			t.Fatal("shared confirmation never reached the lock", err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatal("confirmation escaped exclusive admission lock", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err := tx.Exec(f.ctx, `DELETE FROM np_plugin_webhook_intents WHERE id=$1`, accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, plugindispatch.ErrConflict) {
			t.Fatal("missing identity was inserted under shared lock", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("confirmation did not resume")
	}
	var rows int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM np_plugin_webhook_intents`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("confirmation recreated deleted evidence", rows, err)
	}
}
