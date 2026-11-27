package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginshared"
)

func webhookMaintenanceState(t *testing.T, state string) (*webhookFixture, string, []byte) {
	t.Helper()
	f := newWebhookFixture(t)
	a := f.accept(t, "notify")
	if state != "pending" {
		f.success(t)
		i := f.claim(t)
		if state != "sending" {
			out := plugindispatch.Outcome{Code: "transport_unconfirmed"}
			if state == "http_confirmed" {
				out = plugindispatch.Outcome{Code: "http_2xx", HTTPStatus: 204, ResponseDigest: plugineffects.Digest(nil)}
			}
			if err := f.webhooks.Finish(f.ctx, i, out); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(state, "reviewed_") {
				if _, err := f.webhooks.Review(f.ctx, plugindispatch.ReviewRequest{ID: i.ID, Version: 3, ActorID: f.user.ID, Decision: strings.TrimPrefix(state, "reviewed_"), Reason: "receiver records checked"}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	var raw []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, a.OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return f, a.OperationID, raw
}

func webhookMaintenanceCapture(t *testing.T, f *webhookFixture, key string) pluginshared.CaptureRequest {
	t.Helper()
	p, err := f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{Type: pluginshared.WebhookIntent})
	if err != nil || p.Fatal != "" || len(p.Items) != 1 || p.Items[0].Fault == "" {
		t.Fatal(p, err)
	}
	raw, _ := json.Marshal(p)
	for _, private := range []string{"private-path", "secret body", "fingerprint", "attempt_token", "actor_id", "epoch", `"slot"`, `"url"`} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("diagnostic exposed private facts", private)
		}
	}
	return pluginshared.CaptureRequest{Type: pluginshared.WebhookIntent, Reference: p.Items[0].Reference, Key: key, Operator: "offline-operator", Reason: "verified complete original and SQL index"}
}

func TestPostgreSQLWebhookMaintenanceStatesNeverReplay(t *testing.T) {
	for _, state := range []string{"pending", "sending", "uncertain", "http_confirmed", "reviewed_received", "reviewed_not_received"} {
		t.Run(state, func(t *testing.T) {
			f, id, original := webhookMaintenanceState(t, state)
			f.exec(t, `UPDATE np_plugin_webhook_intents SET slot='wrong-index' WHERE id=$1`, id)
			request := webhookMaintenanceCapture(t, f, "state-"+state)
			q, err := f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			var isolated []byte
			if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_shared_quarantines WHERE id=$1`, q.ID).Scan(&isolated); err != nil {
				t.Fatal(err)
			}
			var saved pluginshared.Quarantine
			if json.Unmarshal(isolated, &saved) != nil || !bytes.Equal(saved.Original.Data, original) {
				t.Fatal("isolation changed original")
			}
			if err = f.webhooks.Ready(f.ctx); err != nil {
				t.Fatal("valid isolated evidence cannot be read", err)
			}
			if _, _, err = f.webhooks.Claim(f.ctx); !errors.Is(err, pluginshared.ErrQuarantined) {
				t.Fatal("isolated operation allowed new send", err)
			}
			if _, err = f.webhooks.Enqueue(f.ctx, f.req); !errors.Is(err, pluginshared.ErrQuarantined) {
				t.Fatal("removed operation was recreated", err)
			}
			repair := pluginshared.RepairRequest{ID: q.ID, Operator: "offline-operator", Reason: "complete original request and attempt verified"}
			fixed, err := f.jobs.RepairSharedRecord(f.ctx, repair, time.Now().UTC())
			if err != nil || fixed.Repair == nil {
				t.Fatal(fixed, err)
			}
			var actual []byte
			if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, id).Scan(&actual); err != nil {
				t.Fatal(err)
			}
			var i plugindispatch.Intent
			if json.Unmarshal(actual, &i) != nil || plugindispatch.Validate(i) != nil {
				t.Fatal("invalid repaired intent")
			}
			if state == "pending" {
				job, err := scanTask(f.pool.QueryRow(f.ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1`, i.JobID))
				if err != nil || job.Status != "cancelled" || job.LeaseToken != "" || i.State != "cancelled" || i.Outcome.Code != "maintenance" {
					t.Fatal("pending repair retained authorization", i.State, job.Status, err)
				}
			} else if state == "sending" {
				if i.State != "uncertain" || i.Outcome.Code != "maintenance" || i.AttemptToken == "" {
					t.Fatal("sending repair lost uncertain fact")
				}
				var oldAttempt plugindispatch.Intent
				if json.Unmarshal(original, &oldAttempt) != nil {
					t.Fatal("invalid original attempt")
				}
				if err = f.webhooks.Finish(f.ctx, oldAttempt, plugindispatch.Outcome{Code: "http_2xx", HTTPStatus: 204, ResponseDigest: plugineffects.Digest(nil)}); !errors.Is(err, plugindispatch.ErrConflict) {
					t.Fatal("late worker replaced maintenance uncertainty", err)
				}
			} else if !bytes.Equal(actual, original) {
				t.Fatal("terminal bytes changed")
			}
			f.assertNoClaim(t)
			if _, err = f.jobs.RepairSharedRecord(f.ctx, repair, time.Now().UTC()); err != nil {
				t.Fatal("repeat confirmation failed", err)
			}
			var beforeRestore []byte
			if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_shared_quarantines WHERE id=$1`, q.ID).Scan(&beforeRestore); err != nil {
				t.Fatal(err)
			}
			if err = f.jobs.CancelRestored(f.ctx, time.Now().UTC()); err != nil {
				t.Fatal("restore failed after repaired intent", err)
			}
			var after []byte
			if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_shared_quarantines WHERE id=$1`, q.ID).Scan(&after); err != nil || !bytes.Equal(beforeRestore, after) {
				t.Fatal("restore changed repair evidence", err)
			}
			if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, id).Scan(&after); err != nil || !bytes.Equal(actual, after) {
				t.Fatal("restore changed repaired terminal", err)
			}
			f.assertNoClaim(t)
		})
	}
}

func TestPostgreSQLWebhookMaintenanceRollbackAndStaleReference(t *testing.T) {
	for _, stage := range []string{"capture", "delete", "repair", "cancel", "audit"} {
		t.Run(stage, func(t *testing.T) {
			f, id, original := webhookMaintenanceState(t, "pending")
			f.exec(t, `UPDATE np_plugin_webhook_intents SET slot='wrong-index' WHERE id=$1`, id)
			request := webhookMaintenanceCapture(t, f, "rollback")
			var q pluginshared.QuarantineSummary
			var err error
			if stage != "capture" && stage != "delete" {
				q, err = f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
			}
			table, action := "np_plugin_shared_quarantines", "INSERT"
			switch stage {
			case "delete":
				table, action = "np_plugin_webhook_intents", "DELETE"
			case "repair":
				table, action = "np_plugin_webhook_intents", "INSERT"
			case "cancel":
				table, action = "np_plugin_jobs", "UPDATE"
			case "audit":
				action = "UPDATE"
			}
			f.exec(t, `CREATE FUNCTION reject_webhook_maintenance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected maintenance failure'; END $$`)
			f.exec(t, `CREATE TRIGGER reject_maintenance BEFORE `+action+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_webhook_maintenance()`)
			if stage == "capture" || stage == "delete" {
				_, err = f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
			} else {
				_, err = f.jobs.RepairSharedRecord(f.ctx, pluginshared.RepairRequest{ID: q.ID, Operator: "offline", Reason: "checked original"}, time.Now().UTC())
			}
			if err == nil {
				t.Fatal("failure injection committed")
			}
			f.exec(t, `DROP TRIGGER reject_maintenance ON `+table)
			var rows, isolated, repaired int
			if err = f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM np_plugin_webhook_intents),(SELECT count(*) FROM np_plugin_shared_quarantines),(SELECT count(*) FROM np_plugin_shared_quarantines WHERE repaired)`).Scan(&rows, &isolated, &repaired); err != nil {
				t.Fatal(err)
			}
			if stage == "capture" || stage == "delete" {
				if rows != 1 || isolated != 0 {
					t.Fatal("partial isolation", rows, isolated)
				}
				var raw []byte
				if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, id).Scan(&raw); err != nil || !bytes.Equal(raw, original) {
					t.Fatal("original changed", err)
				}
			} else if rows != 0 || isolated != 1 || repaired != 0 {
				t.Fatal("partial repair", rows, isolated, repaired)
			}
			job, err := scanTask(f.pool.QueryRow(f.ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1`, f.req.Authorization.Claim.ID))
			if err != nil || job.Status != "running" {
				t.Fatal("failed transaction cancelled task", job.Status, err)
			}
		})
	}
	f, id, _ := webhookMaintenanceState(t, "pending")
	f.exec(t, `UPDATE np_plugin_webhook_intents SET slot='wrong-index' WHERE id=$1`, id)
	request := webhookMaintenanceCapture(t, f, "stale")
	f.exec(t, `UPDATE np_plugin_webhook_intents SET slot='different-index' WHERE id=$1`, id)
	if _, err := f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC()); !errors.Is(err, pluginshared.ErrConflict) {
		t.Fatal("stale reference removed a different record", err)
	}
}

func TestPostgreSQLWebhookMaintenanceUnprovableEvidenceAndDuplicateRepair(t *testing.T) {
	f, id, original := webhookMaintenanceState(t, "pending")
	f.exec(t, `INSERT INTO np_plugin_webhook_intents(id,job_id,slot,version,state,data) SELECT $2,job_id,'duplicate-index',version,state,data FROM np_plugin_webhook_intents WHERE id=$1`, id, strings.Repeat("9", 64))
	p, err := f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{Type: pluginshared.WebhookIntent})
	if err != nil || len(p.Items) != 2 || p.Items[0].Fault != "duplicate_identity" || p.Items[1].Fault != "duplicate_identity" {
		t.Fatal(p, err)
	}
	capture := func(ref, key string) pluginshared.QuarantineSummary {
		t.Helper()
		q, err := f.jobs.QuarantineSharedRecord(f.ctx, pluginshared.CaptureRequest{Type: pluginshared.WebhookIntent, Reference: ref, Key: key, Operator: "offline", Reason: "duplicate request identity"}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	first := capture(p.Items[0].Reference, "duplicate-first")
	p, err = f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{Type: pluginshared.WebhookIntent})
	if err != nil || len(p.Items) != 1 || p.Items[0].Fault != "duplicate_identity" {
		t.Fatal("hidden duplicate after isolation", p, err)
	}
	request := pluginshared.RepairRequest{ID: first.ID, Operator: "offline", Reason: "same request", KeepReference: p.Items[0].Reference}
	if _, err = f.jobs.RepairSharedRecord(f.ctx, request, time.Now().UTC()); err == nil {
		t.Fatal("kept pending duplicate")
	}
	second := capture(p.Items[0].Reference, "duplicate-second")
	request.KeepReference = ""
	if _, err = f.jobs.RepairSharedRecord(f.ctx, request, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p, err = f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{Type: pluginshared.WebhookIntent})
	if err != nil || len(p.Items) != 1 {
		t.Fatal(p, err)
	}
	request.ID, request.KeepReference = second.ID, p.Items[0].Reference
	if _, err = f.jobs.RepairSharedRecord(f.ctx, request, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	f.assertNoClaim(t)
	// Malformed original cannot be repaired using a merely plausible old payload.
	f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$2 WHERE id=$1`, id, []byte(`{`))
	bad := webhookMaintenanceCapture(t, f, "unprovable")
	q, err := f.jobs.QuarantineSharedRecord(f.ctx, bad, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.jobs.RepairSharedRecord(f.ctx, pluginshared.RepairRequest{ID: q.ID, Operator: "offline", Reason: "candidate backup", Replacement: original, ReplacementDigest: plugineffects.Digest(original)}, time.Now().UTC()); err == nil {
		t.Fatal("missing original facts invented")
	}
	if err = f.jobs.CancelRestored(f.ctx, time.Now().UTC()); err != nil {
		t.Fatal("restore lost isolated evidence", err)
	}
	if _, _, err = f.webhooks.Claim(f.ctx); !errors.Is(err, pluginshared.ErrQuarantined) {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE np_plugin_shared_quarantines SET repaired=true WHERE id=$1`, q.ID)
	p, err = f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{})
	if err != nil || p.Fatal != "invalid_quarantine" {
		t.Fatal("index tamper hid evidence", p, err)
	}
}

func TestPostgreSQLWebhookMaintenanceSerializesWithClaimAndFinish(t *testing.T) {
	f, id, _ := webhookMaintenanceState(t, "pending")
	f.success(t)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = lockSharedMaintenance(f.ctx, tx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 200*time.Millisecond)
	defer cancel()
	if _, ok, err := f.webhooks.Claim(ctx); err == nil || ok {
		t.Fatal("claim crossed exclusive maintenance boundary", ok, err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	i := f.claim(t)
	if i.ID != id {
		t.Fatal("identity changed")
	}
	// A sender result held by maintenance must wait, then preserve its fact.
	tx, err = f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = lockSharedMaintenance(f.ctx, tx); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(f.ctx, 200*time.Millisecond)
	defer cancel2()
	if err = f.webhooks.Finish(ctx2, i, plugindispatch.Outcome{Code: "transport_unconfirmed"}); err == nil {
		t.Fatal("finish bypassed maintenance lock")
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.webhooks.Finish(f.ctx, i, plugindispatch.Outcome{Code: "transport_unconfirmed"}); err != nil {
		t.Fatal("retry storing result lost fact", err)
	}
	f.assertNoClaim(t)
}

func TestPostgreSQLWebhookMaintenanceRepairCapacityIsAtomic(t *testing.T) {
	for _, mode := range []string{"separate-task-slots", "reserved-result-bytes"} {
		t.Run(mode, func(t *testing.T) {
			f := newWebhookFixture(t)
			if mode == "reserved-result-bytes" {
				f.req.Body = json.RawMessage(`{"message":"` + strings.Repeat("x", 15000) + `"}`)
			}
			a := f.accept(t, "notify")
			var raw []byte
			if err := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, a.OperationID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var original plugindispatch.Intent
			if json.Unmarshal(raw, &original) != nil {
				t.Fatal("invalid original")
			}
			f.exec(t, `UPDATE np_plugin_webhook_intents SET slot='wrong-index' WHERE id=$1`, a.OperationID)
			q, err := f.jobs.QuarantineSharedRecord(f.ctx, webhookMaintenanceCapture(t, f, "capacity"), time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			count := plugindispatch.MaxSlotsPerTask
			if mode == "reserved-result-bytes" {
				count = plugindispatch.MaxTotalBytes / plugindispatch.BudgetBytes(original)
			}
			rows := make([][]any, 0, count)
			lastID := ""
			for n := range count {
				i := original
				i.Slot = fmt.Sprintf("retained-%d", n)
				if mode == "reserved-result-bytes" {
					i.JobID = fmt.Sprintf("%064x", n+1)
				}
				i.ID, err = plugindispatch.OperationID(i.JobID, i.Slot)
				if err != nil || plugindispatch.Validate(i) != nil {
					t.Fatal("invalid budget fixture", err)
				}
				data, _ := json.Marshal(i)
				rows = append(rows, []any{i.ID, i.JobID, i.Slot, i.Version, i.State, data})
				lastID = i.ID
				if mode == "separate-task-slots" {
					id, _ := plugineffects.OperationID(i.JobID, i.Slot)
					receipt := plugineffects.Receipt{Schema: 1, JobID: i.JobID, Plugin: i.Plugin, Slot: i.Slot, ActorID: i.ActorID, Fingerprint: i.Fingerprint, ManifestDigest: i.ManifestDigest, ModuleDigest: i.ModuleDigest,
						Result: plugineffects.Result{OperationID: id, PostID: f.post.ID, PreviousVersion: 1, Version: 2, Changed: true, CommittedAt: i.CreatedAt}}
					data, _ := json.Marshal(receipt)
					f.exec(t, `INSERT INTO np_plugin_content_effects(id,job_id,slot,data) VALUES($1,$2,$3,$4)`, id, i.JobID, i.Slot, data)
				}
			}
			if _, err = f.pool.CopyFrom(f.ctx, pgx.Identifier{"np_plugin_webhook_intents"}, []string{"id", "job_id", "slot", "version", "state", "data"}, pgx.CopyFromRows(rows)); err != nil {
				t.Fatal(err)
			}
			repair := pluginshared.RepairRequest{ID: q.ID, Operator: "offline", Reason: "retain original and respect independent budgets"}
			if _, err = f.jobs.RepairSharedRecord(f.ctx, repair, time.Now().UTC()); !errors.Is(err, pluginshared.ErrCapacity) {
				t.Fatal("repair bypassed reserved capacity", mode, err)
			}
			var repaired bool
			var restored int
			if err = f.pool.QueryRow(f.ctx, `SELECT repaired,(SELECT count(*) FROM np_plugin_webhook_intents WHERE id=$2) FROM np_plugin_shared_quarantines WHERE id=$1`, q.ID, a.OperationID).Scan(&repaired, &restored); err != nil || repaired || restored != 0 {
				t.Fatal("capacity failure partially committed", repaired, restored, err)
			}
			job, err := scanTask(f.pool.QueryRow(f.ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1`, original.JobID))
			if err != nil || job.Status != "running" {
				t.Fatal("capacity failure cancelled source", job.Status, err)
			}
			// Removing only the synthetic fixture lets repair fit exactly. The
			// production maintenance command never evicts a retained operation.
			f.exec(t, `DELETE FROM np_plugin_webhook_intents WHERE id=$1`, lastID)
			fixed, err := f.jobs.RepairSharedRecord(f.ctx, repair, time.Now().UTC())
			if err != nil || fixed.Repair == nil || fixed.Repair.Disposition != "webhook_cancelled" {
				t.Fatal("valid budget boundary rejected", mode, err)
			}
		})
	}
}
