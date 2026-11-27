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

	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/pluginshared"
)

func sharedDamage(t *testing.T, f *contentEffectFixture, kind string) (pluginshared.CaptureRequest, []byte) {
	t.Helper()
	var original []byte
	if kind == pluginshared.Authority {
		if err := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_task_authorizations WHERE slug=$1`, f.control.Slug).Scan(&original); err != nil {
			t.Fatal(err)
		}
		f.exec(t, `UPDATE np_plugin_task_authorizations SET enabled=false WHERE slug=$1`, f.control.Slug)
	} else {
		result, err := f.effects.WriteContent(f.ctx, f.request)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_content_effects WHERE id=$1`, result.OperationID).Scan(&original); err != nil {
			t.Fatal(err)
		}
		f.exec(t, `UPDATE np_plugin_content_effects SET slot='moved-index' WHERE id=$1`, result.OperationID)
	}
	page, err := f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{Type: kind})
	if err != nil || page.Fatal != "" || len(page.Items) != 1 || page.Items[0].Fault != "index_mismatch" {
		t.Fatal(page, err)
	}
	return pluginshared.CaptureRequest{Type: kind, Reference: page.Items[0].Reference, Key: "isolate-" + kind, Operator: "offline-operator", Reason: "SQL index mismatch verified"}, original
}
func TestPostgreSQLPluginSharedMaintenancePreservesFactsAndFencesWork(t *testing.T) {
	for _, kind := range []string{pluginshared.Authority, pluginshared.ContentEffect} {
		t.Run(kind, func(t *testing.T) {
			f := newContentEffectFixture(t, false)
			request, original := sharedDamage(t, f, kind)
			q, err := f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			repeat, err := f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
			if err != nil || repeat.ID != q.ID || !repeat.CapturedAt.Equal(q.CapturedAt) {
				t.Fatal("capture repeat", repeat, err)
			}
			changed := request
			changed.Reason = "another reason"
			if _, err = f.jobs.QuarantineSharedRecord(f.ctx, changed, time.Now().UTC()); !errors.Is(err, pluginshared.ErrConflict) {
				t.Fatal("changed capture", err)
			}
			var evidence []byte
			if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_shared_quarantines WHERE id=$1`, q.ID).Scan(&evidence); err != nil {
				t.Fatal(err)
			}
			var preserved pluginshared.Quarantine
			if json.Unmarshal(evidence, &preserved) != nil || !bytes.Equal(preserved.Original.Data, original) || pluginshared.ValidateQuarantine(preserved) != nil {
				t.Fatal("original bytes lost")
			}
			if err = f.controls.Ready(f.ctx); err != nil {
				t.Fatal("isolated authority cannot open for review", err)
			}
			if err = f.effects.Ready(f.ctx); err != nil {
				t.Fatal("isolated receipt cannot open for review", err)
			}
			if _, _, err = f.controls.LoadTaskControl(f.ctx, f.control.Slug); !errors.Is(err, pluginshared.ErrQuarantined) {
				t.Fatal("shared read bypass", err)
			}
			if _, err = f.controls.ListTaskControls(f.ctx); !errors.Is(err, pluginshared.ErrQuarantined) {
				t.Fatal("subscription discovery bypass", err)
			}
			next := f.control
			next.Revision++
			if err = f.controls.SaveTaskControl(f.ctx, f.control.Revision, next); !errors.Is(err, pluginshared.ErrQuarantined) {
				t.Fatal("authority write bypass", err)
			}
			claim := f.request.Authorization.Claim
			for _, check := range []func() error{
				func() error { return f.jobs.CheckClaim(f.ctx, claim, time.Now().UTC()) },
				func() error { return f.jobs.CheckSharedClaim(f.ctx, claim, time.Now().UTC()) },
				func() error { _, _, e := f.jobs.Claim(f.ctx, time.Now().UTC()); return e },
				func() error {
					_, _, e := f.jobs.ClaimScoped(f.ctx, time.Now().UTC(), []pluginjobs.ClaimScope{{Plugin: f.control.Slug, Epoch: f.control.Epoch.Token}})
					return e
				},
				func() error { _, e := f.effects.WriteContent(f.ctx, f.request); return e },
				func() error { return f.jobs.Finish(f.ctx, claim, "", time.Now().UTC()) },
				func() error {
					_, e := f.jobs.Enqueue(f.ctx, pluginjobs.Input{Plugin: f.control.Slug, Epoch: f.control.Epoch.Token, Kind: "task.execute.v1", ActorID: f.user.ID, Key: "new-shared-task", Payload: json.RawMessage(`{}`)}, time.Now().UTC())
					return e
				},
				func() error {
					_, e := f.jobs.FreezeFanout(f.ctx, pluginjobs.Event{Key: "new-shared-event", Kind: "content.updated", ObjectID: f.post.ID, Version: 2, At: time.Now().UTC()}, nil)
					return e
				},
			} {
				if e := check(); !errors.Is(e, pluginshared.ErrQuarantined) {
					t.Fatal("shared work bypassed unresolved evidence", e)
				}
			}
			repair := pluginshared.RepairRequest{ID: q.ID, Operator: "offline-operator", Reason: "original JSON checked"}
			fixed, err := f.jobs.RepairSharedRecord(f.ctx, repair, time.Now().UTC())
			if err != nil || fixed.Repair == nil {
				t.Fatal(fixed, err)
			}
			var first []byte
			if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_shared_quarantines WHERE id=$1`, q.ID).Scan(&first); err != nil {
				t.Fatal(err)
			}
			if _, err = f.jobs.RepairSharedRecord(f.ctx, repair, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			var again []byte
			if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_shared_quarantines WHERE id=$1`, q.ID).Scan(&again); err != nil || !bytes.Equal(first, again) {
				t.Fatal("repeat repair rewrote evidence", err)
			}
			if kind == pluginshared.Authority {
				control, found, e := f.controls.LoadTaskControl(f.ctx, f.control.Slug)
				if e != nil || !found || control.Enabled || !control.Retired || control.Epoch.ActorID != 0 || control.Revision != f.control.Revision+1 || control.Epoch.Token == f.control.Epoch.Token {
					t.Fatal("authority was revived", control, e)
				}
				if _, e = f.effects.WriteContent(f.ctx, f.request); e == nil {
					t.Fatal("old authority wrote")
				}
				f.assertCounts(t, 1, 1, 0, 0, 0)
			} else {
				id, _, _ := f.request.Identity()
				var actual []byte
				if e := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_content_effects WHERE id=$1`, id).Scan(&actual); e != nil || !bytes.Equal(actual, original) {
					t.Fatal("receipt was altered", e)
				}
				job, e := scanTask(f.pool.QueryRow(f.ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1`, claim.ID))
				if e != nil || job.Status != "cancelled" || job.LeaseToken != "" {
					t.Fatal("receipt repair revived execution", job, e)
				}
				f.assertCounts(t, 2, 2, 1, 1, 1)
			}
			if err = f.jobs.CancelRestored(f.ctx, time.Now().UTC()); err != nil {
				t.Fatal("restore after repair", err)
			}
			if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_shared_quarantines WHERE id=$1`, q.ID).Scan(&again); err != nil || !bytes.Equal(first, again) {
				t.Fatal("restore rewrote evidence", err)
			}
		})
	}
}

func TestPostgreSQLPluginSharedMaintenanceTransactionRollbackAndStaleReference(t *testing.T) {
	for _, stage := range []string{"quarantine", "delete", "repair", "cancel", "audit"} {
		t.Run(stage, func(t *testing.T) {
			f := newContentEffectFixture(t, false)
			request, original := sharedDamage(t, f, pluginshared.ContentEffect)
			id, _, _ := f.request.Identity()
			var q pluginshared.QuarantineSummary
			var err error
			if stage != "quarantine" && stage != "delete" {
				q, err = f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
			}
			table, event := "np_plugin_shared_quarantines", "INSERT"
			switch stage {
			case "delete":
				table, event = "np_plugin_content_effects", "DELETE"
			case "repair":
				table, event = "np_plugin_content_effects", "INSERT"
			case "cancel":
				table, event = "np_plugin_jobs", "UPDATE"
			case "audit":
				event = "UPDATE"
			}
			f.exec(t, `CREATE FUNCTION fail_shared_maintenance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected shared maintenance failure'; END $$`)
			f.exec(t, `CREATE TRIGGER fail_shared BEFORE `+event+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION fail_shared_maintenance()`)
			if stage == "quarantine" || stage == "delete" {
				_, err = f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
			} else {
				_, err = f.jobs.RepairSharedRecord(f.ctx, pluginshared.RepairRequest{ID: q.ID, Operator: "offline-operator", Reason: "checked original"}, time.Now().UTC())
			}
			if err == nil {
				t.Fatal("injected failure committed")
			}
			f.exec(t, `DROP TRIGGER fail_shared ON `+table)
			var rows, isolated, repaired int
			if e := f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM np_plugin_content_effects),(SELECT count(*) FROM np_plugin_shared_quarantines),(SELECT count(*) FROM np_plugin_shared_quarantines WHERE repaired)`).Scan(&rows, &isolated, &repaired); e != nil {
				t.Fatal(e)
			}
			if stage == "quarantine" || stage == "delete" {
				if rows != 1 || isolated != 0 {
					t.Fatal("partial isolation", rows, isolated)
				}
				var raw []byte
				if e := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_content_effects WHERE id=$1`, id).Scan(&raw); e != nil || !bytes.Equal(original, raw) {
					t.Fatal("original lost", e)
				}
			} else {
				if rows != 0 || isolated != 1 || repaired != 0 {
					t.Fatal("partial repair", rows, isolated, repaired)
				}
			}
			job, e := scanTask(f.pool.QueryRow(f.ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1`, f.request.Authorization.Claim.ID))
			if e != nil || job.Status != "running" {
				t.Fatal("failure cancelled task", job, e)
			}
		})
	}
	f := newContentEffectFixture(t, false)
	request, _ := sharedDamage(t, f, pluginshared.Authority)
	f.exec(t, `UPDATE np_plugin_task_authorizations SET revision=revision+1 WHERE slug=$1`, f.control.Slug)
	if _, err := f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC()); !errors.Is(err, pluginshared.ErrConflict) {
		t.Fatal("stale reference removed new record", err)
	}
}

func TestPostgreSQLPluginSharedMaintenanceConcurrentCaptureAndRepair(t *testing.T) {
	f := newContentEffectFixture(t, false)
	request, _ := sharedDamage(t, f, pluginshared.ContentEffect)
	var wg sync.WaitGroup
	results := make(chan pluginshared.QuarantineSummary, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q, e := f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
			results <- q
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id string
	for q := range results {
		if id != "" && id != q.ID {
			t.Fatal("two quarantine identities")
		}
		id = q.ID
	}
	repair := pluginshared.RepairRequest{ID: id, Operator: "offline-operator", Reason: "verified original"}
	errs = make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.jobs.RepairSharedRecord(f.ctx, repair, time.Now().UTC()); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.assertCounts(t, 2, 2, 1, 1, 1)
}

func TestPostgreSQLPluginSharedMaintenanceRestoreAndEvidenceIntegrity(t *testing.T) {
	f := newContentEffectFixture(t, false)
	request, _ := sharedDamage(t, f, pluginshared.ContentEffect)
	q, err := f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_shared_quarantines WHERE id=$1`, q.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err = f.jobs.CancelRestored(f.ctx, time.Now().UTC()); err != nil {
		t.Fatal("restore must retain unresolved gate", err)
	}
	var after []byte
	if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_shared_quarantines WHERE id=$1`, q.ID).Scan(&after); err != nil || !bytes.Equal(original, after) {
		t.Fatal("restore changed quarantine", err)
	}
	for _, column := range []string{"repaired", "record_type", "reference", "captured_at", "data"} {
		t.Run(column, func(t *testing.T) {
			switch column {
			case "repaired":
				f.exec(t, `UPDATE np_plugin_shared_quarantines SET repaired=true WHERE id=$1`, q.ID)
			case "record_type":
				f.exec(t, `UPDATE np_plugin_shared_quarantines SET record_type='authority' WHERE id=$1`, q.ID)
			case "reference":
				f.exec(t, `UPDATE np_plugin_shared_quarantines SET reference=$2 WHERE id=$1`, q.ID, strings.Repeat("e", 64))
			case "captured_at":
				f.exec(t, `UPDATE np_plugin_shared_quarantines SET captured_at=captured_at+interval '1 second' WHERE id=$1`, q.ID)
			case "data":
				f.exec(t, `UPDATE np_plugin_shared_quarantines SET data=$2 WHERE id=$1`, q.ID, []byte(`{`))
			}
			if err := f.effects.Ready(f.ctx); err == nil {
				t.Fatal("bad evidence readiness")
			}
			if err := f.controls.Ready(f.ctx); err == nil {
				t.Fatal("bad authority evidence readiness")
			}
			if _, _, err := f.jobs.Claim(f.ctx, time.Now().UTC()); err == nil {
				t.Fatal("corrupt gate allowed claim")
			}
			if err := f.jobs.CancelRestored(f.ctx, time.Now().UTC()); err == nil {
				t.Fatal("corrupt evidence allowed restore")
			}
			page, err := f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{})
			if err != nil || page.Fatal != "invalid_quarantine" {
				t.Fatal(page, err)
			}
			f.exec(t, `UPDATE np_plugin_shared_quarantines SET record_type=$2,reference=$3,captured_at=$4,repaired=false,data=$5 WHERE id=$1`, q.ID, q.Type, q.Reference, q.CapturedAt, original)
		})
	}
}

func TestPostgreSQLPluginSharedMaintenanceDuplicateAuthorityAndReceipt(t *testing.T) {
	for _, kind := range []string{pluginshared.Authority, pluginshared.ContentEffect} {
		t.Run(kind, func(t *testing.T) {
			f := newContentEffectFixture(t, false)
			if kind == pluginshared.Authority {
				f.exec(t, `INSERT INTO np_plugin_task_authorizations SELECT 'wrong-alias',revision,epoch,enabled,retired,data FROM np_plugin_task_authorizations WHERE slug=$1`, f.control.Slug)
			} else {
				r, err := f.effects.WriteContent(f.ctx, f.request)
				if err != nil {
					t.Fatal(err)
				}
				f.exec(t, `INSERT INTO np_plugin_content_effects SELECT $2,$3,'duplicate-slot',data FROM np_plugin_content_effects WHERE id=$1`, r.OperationID, strings.Repeat("c", 64), strings.Repeat("d", 64))
			}
			page, err := f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{Type: kind})
			if err != nil || len(page.Items) != 2 {
				t.Fatal(page, err)
			}
			for _, d := range page.Items {
				if d.Fault != "duplicate_identity" {
					t.Fatal("duplicate invisible", d)
				}
			}
			var duplicate pluginshared.Diagnostic
			for _, d := range page.Items {
				if d.IndexMismatch {
					duplicate = d
				}
			}
			q, err := f.jobs.QuarantineSharedRecord(f.ctx, pluginshared.CaptureRequest{Type: kind, Reference: duplicate.Reference, Key: "duplicate", Operator: "offline-operator", Reason: "same identity checked"}, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			page, err = f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{Type: kind})
			if err != nil || len(page.Items) != 1 || page.Items[0].Fault != "duplicate_identity" {
				t.Fatal("isolated duplicate disappeared", page, err)
			}
			if kind == pluginshared.Authority {
				q2, e := f.jobs.QuarantineSharedRecord(f.ctx, pluginshared.CaptureRequest{Type: kind, Reference: page.Items[0].Reference, Key: "original-copy", Operator: "offline-operator", Reason: "retire duplicate authority"}, time.Now().UTC())
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.jobs.RepairSharedRecord(f.ctx, pluginshared.RepairRequest{ID: q2.ID, Operator: "offline-operator", Reason: "original authority checked"}, time.Now().UTC()); e != nil {
					t.Fatal(e)
				}
				page, e = f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{Type: kind})
				if e != nil || len(page.Items) != 1 {
					t.Fatal(page, e)
				}
			}
			_, err = f.jobs.RepairSharedRecord(f.ctx, pluginshared.RepairRequest{ID: q.ID, Operator: "offline-operator", Reason: "retained same facts checked", KeepReference: page.Items[0].Reference}, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if err = f.controls.Ready(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err = f.effects.Ready(f.ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgreSQLPluginSharedMaintenanceLockOrder(t *testing.T) {
	f := newContentEffectFixture(t, false)
	request, _ := sharedDamage(t, f, pluginshared.Authority)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(f.ctx, `LOCK TABLE np_plugin_jobs IN ROW SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 250*time.Millisecond)
	defer cancel()
	if _, err = f.jobs.QuarantineSharedRecord(ctx, request, time.Now().UTC()); err == nil {
		t.Fatal("maintenance passed live task fence")
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	q, err := f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.jobs.RepairSharedRecord(f.ctx, pluginshared.RepairRequest{ID: q.ID, Operator: "offline-operator", Reason: "lock boundary checked"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQLPluginSharedMaintenanceBudgetsAndUnprovableReplacement(t *testing.T) {
	for _, mode := range []string{"record-size", "quarantine-size", "quarantine-count", "unprovable"} {
		t.Run(mode, func(t *testing.T) {
			f := newContentEffectFixture(t, false)
			switch mode {
			case "record-size":
				f.exec(t, `UPDATE np_plugin_task_authorizations SET data=$1`, bytes.Repeat([]byte(" "), pluginshared.MaxRawBytes+1))
				page, err := f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{})
				if err != nil || page.Fatal != "storage_capacity" {
					t.Fatal(page, err)
				}
			case "quarantine-size", "quarantine-count":
				request, _ := sharedDamage(t, f, pluginshared.ContentEffect)
				q, err := f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if mode == "quarantine-size" {
					f.exec(t, `UPDATE np_plugin_shared_quarantines SET data=$1`, bytes.Repeat([]byte(" "), pluginshared.MaxQuarantineRecordBytes+1))
				} else {
					f.exec(t, `INSERT INTO np_plugin_shared_quarantines SELECT repeat(md5(g::text),2),record_type,reference,captured_at,repaired,data FROM np_plugin_shared_quarantines CROSS JOIN generate_series(1,$1) g`, pluginshared.MaxQuarantines)
				}
				page, err := f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{})
				if err != nil || page.Fatal != "invalid_quarantine" {
					t.Fatal(page, err)
				}
				if _, err = f.jobs.RepairSharedRecord(f.ctx, pluginshared.RepairRequest{ID: q.ID, Operator: "offline-fixture", Reason: "must retain oversized evidence"}, time.Now().UTC()); err == nil {
					t.Fatal("oversized quarantine repaired")
				}
			case "unprovable":
				request, original := sharedDamage(t, f, pluginshared.ContentEffect)
				f.exec(t, `UPDATE np_plugin_content_effects SET data=$1`, []byte(`{}`))
				page, err := f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{Type: pluginshared.ContentEffect})
				if err != nil || len(page.Items) != 1 {
					t.Fatal(page, err)
				}
				request.Reference = page.Items[0].Reference
				q, err := f.jobs.QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.jobs.RepairSharedRecord(f.ctx, pluginshared.RepairRequest{ID: q.ID, Operator: "offline-fixture", Reason: "cannot infer lost facts", Replacement: original, ReplacementDigest: plugineffects.Digest(original)}, time.Now().UTC()); !errors.Is(err, pluginshared.ErrConflict) {
					t.Fatal("replacement invented facts", err)
				}
				if _, _, err = f.jobs.Claim(f.ctx, time.Now().UTC()); !errors.Is(err, pluginshared.ErrQuarantined) {
					t.Fatal("failed repair removed execution gate", err)
				}
			}
		})
	}
}

func TestPostgreSQLPluginSharedMaintenanceWaitsForContentCommit(t *testing.T) {
	f := newContentEffectFixture(t, false)
	writerName, maintenanceName := fmt.Sprintf("shared-writer-%d", time.Now().UnixNano()), fmt.Sprintf("shared-maintenance-%d", time.Now().UnixNano())
	writer, maintenance := contentEffectNamedPool(t, f, writerName), contentEffectNamedPool(t, f, maintenanceName)
	blocker, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(f.ctx, `SELECT id FROM np_posts WHERE id=$1 FOR UPDATE`, f.post.ID); err != nil {
		t.Fatal(err)
	}
	written, isolated := make(chan error, 1), make(chan error, 1)
	go func() { _, e := NewPluginContentEffectRepository(writer).WriteContent(f.ctx, f.request); written <- e }()
	waitContentEffectLock(t, f, writerName)
	// Introduce a separate damaged record after the writer's checked snapshot.
	// The maintenance move must wait until that accepted content commit ends.
	id := strings.Repeat("e", 64)
	f.exec(t, `INSERT INTO np_plugin_content_effects(id,job_id,slot,data) VALUES($1,$1,'broken',$2)`, id, []byte(`{`))
	page, err := f.jobs.InspectSharedRecords(f.ctx, pluginshared.Query{Type: pluginshared.ContentEffect})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	request := pluginshared.CaptureRequest{Type: pluginshared.ContentEffect, Reference: page.Items[0].Reference, Key: "concurrent-content", Operator: "offline-fixture", Reason: "verify commit order"}
	go func() {
		_, e := NewPluginJobRepository(maintenance).QuarantineSharedRecord(f.ctx, request, time.Now().UTC())
		isolated <- e
	}()
	waitContentEffectLock(t, f, maintenanceName)
	if err = blocker.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-written; err != nil {
		t.Fatal("accepted write did not finish before quarantine", err)
	}
	if err = <-isolated; err != nil {
		t.Fatal(err)
	}
	f.assertCounts(t, 2, 2, 1, 1, 1)
	if _, err = f.effects.WriteContent(f.ctx, f.request); !errors.Is(err, pluginshared.ErrQuarantined) {
		t.Fatal("later write bypassed quarantine", err)
	}
}
