package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/pluginjobs"
)

func maintenanceDiagnostic(t *testing.T, r *PluginJobRepository, kind pluginjobs.RecordType) pluginjobs.RecordDiagnostic {
	t.Helper()
	page, err := r.InspectRecords(t.Context(), pluginjobs.DiagnosticQuery{Type: kind})
	if err != nil || page.Fatal != "" {
		t.Fatal(page, err)
	}
	for _, item := range page.Items {
		if item.Fault != "" {
			return item
		}
	}
	t.Fatal("damaged record was not diagnosed")
	return pluginjobs.RecordDiagnostic{}
}

func TestPostgreSQLPluginMaintenanceQuarantineAndRepairFourClasses(t *testing.T) {
	for _, kind := range []pluginjobs.RecordType{pluginjobs.RecordTask, pluginjobs.RecordTaskHistory, pluginjobs.RecordFanout, pluginjobs.RecordFanoutHistory} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, pool, r := newPluginJobsIntegrationStore(t)
			now := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute + 123*time.Nanosecond)
			in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "private-maintenance-key", ActorID: 7, Payload: json.RawMessage(`{"private":"original private maintenance payload"}`)}
			j, err := r.Enqueue(ctx, in, now)
			if err != nil {
				t.Fatal(err)
			}
			var oldClaim pluginjobs.Job
			if kind == pluginjobs.RecordTask {
				oldClaim, _, err = r.Claim(ctx, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == pluginjobs.RecordTaskHistory {
				if err = r.Change(ctx, in.Plugin, j.ID, j.Version, "cancel", now); err != nil {
					t.Fatal(err)
				}
				if _, err = r.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Before: now}, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			subs := []pluginjobs.Subscription{{Plugin: in.Plugin, Epoch: in.Epoch, Since: now, ActorID: in.ActorID}}
			if kind == pluginjobs.RecordFanoutHistory {
				subs = []pluginjobs.Subscription{} // empty cohorts are durable facts too
			}
			event := pluginjobs.Event{Key: "private-maintenance-event", Kind: "content.updated", ObjectID: 1, Version: 2, At: now}
			if _, err = r.FreezeFanout(ctx, event, subs); err != nil {
				t.Fatal(err)
			}
			if err = r.CompleteFanout(ctx, event); err != nil {
				t.Fatal(err)
			}
			if kind == pluginjobs.RecordFanoutHistory {
				if _, err = r.Archive(ctx, pluginjobs.ArchiveQuery{Fanouts: true, Before: now}, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			healthy := in
			healthy.Key = "healthy-independent-key"
			healthyJob, err := r.Enqueue(ctx, healthy, now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			var unchanged []byte
			if err = pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, healthyJob.ID).Scan(&unchanged); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case pluginjobs.RecordTask:
				_, err = pool.Exec(ctx, `UPDATE np_plugin_jobs SET plugin='wrong-reader' WHERE id=$1`, j.ID)
			case pluginjobs.RecordTaskHistory:
				_, err = pool.Exec(ctx, `UPDATE np_plugin_job_archives SET plugin='wrong-reader' WHERE id=$1`, j.ID)
			case pluginjobs.RecordFanout:
				_, err = pool.Exec(ctx, `UPDATE np_plugin_fanouts SET event_key='wrong-event-key' WHERE event_key=$1`, event.Key)
			case pluginjobs.RecordFanoutHistory:
				_, err = pool.Exec(ctx, `UPDATE np_plugin_fanout_archives SET cancelled=true WHERE event_key=$1`, event.Key)
			}
			if err != nil {
				t.Fatal(err)
			}
			d := maintenanceDiagnostic(t, r, kind)
			if d.Fault != "index_mismatch" {
				t.Fatal(d)
			}
			request := pluginjobs.QuarantineRequest{Reference: d.Reference, Type: kind, Key: "maintenance-command", Operator: "local-operator", Reason: "SQL index mismatch checked"}
			isolated, err := r.QuarantineRecord(ctx, request, now.Add(2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err = pool.QueryRow(ctx, `SELECT data FROM np_plugin_record_quarantines WHERE id=$1`, isolated.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var evidence pluginjobs.QuarantinedRecord
			if err = json.Unmarshal(raw, &evidence); err != nil || pluginjobs.ValidateQuarantine(evidence) != nil || pluginjobs.RecordReference(evidence.Original) != d.Reference || len(evidence.Original.Metadata) == 0 {
				t.Fatal("complete original/SQL metadata was not retained", err)
			}
			confirmation, err := r.QuarantineRecord(ctx, request, now.Add(3*time.Second))
			if err != nil || confirmation.ID != isolated.ID || !confirmation.CapturedAt.Equal(isolated.CapturedAt) {
				t.Fatal("repeat quarantine changed its event", confirmation, err)
			}
			if kind.IsTask() {
				fresh := in
				fresh.Key = "new-admission-while-isolated"
				if _, err = r.Enqueue(ctx, fresh, now.Add(3*time.Second)); !errors.Is(err, pluginjobs.ErrQuarantined) {
					t.Fatal("new task admission bypassed isolation", err)
				}
				if _, err = r.Enqueue(ctx, healthy, now); err != nil {
					t.Fatal("old confirmation was blocked by new admission gate", err)
				}
				if kind == pluginjobs.RecordTask {
					if err = r.CheckClaim(ctx, oldClaim, now.Add(3*time.Second)); !errors.Is(err, pluginjobs.ErrAuthorization) {
						t.Fatal("old isolated claim remained authorized", err)
					}
				}
			} else {
				fresh := event
				fresh.Key = "new-event-while-isolated"
				if _, err = r.FreezeFanout(ctx, fresh, subs); !errors.Is(err, pluginjobs.ErrQuarantined) {
					t.Fatal("new receipt admission bypassed isolation", err)
				}
			}
			var afterIsolation []byte
			if err = pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, healthyJob.ID).Scan(&afterIsolation); err != nil || !bytes.Equal(unchanged, afterIsolation) {
				t.Fatal("isolation rewrote unrelated task bytes", err)
			}
			repair := pluginjobs.RepairRequest{ID: isolated.ID, Operator: "local-operator", Reason: "JSON authority checked and index rebuilt"}
			repaired, err := r.RepairRecord(ctx, repair, now.Add(4*time.Second))
			if err != nil || repaired.Repair == nil {
				t.Fatal(repaired, err)
			}
			if _, err = r.RepairRecord(ctx, repair, now.Add(5*time.Second)); err != nil {
				t.Fatal("lost-response repair confirmation failed", err)
			}
			if err = pool.QueryRow(ctx, `SELECT data FROM np_plugin_record_quarantines WHERE id=$1`, isolated.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var repairedEvidence pluginjobs.QuarantinedRecord
			if err = json.Unmarshal(raw, &repairedEvidence); err != nil || !reflect.DeepEqual(repairedEvidence.Original, evidence.Original) {
				t.Fatal("repair replaced original evidence", err)
			}
			page, err := r.InspectRecords(ctx, pluginjobs.DiagnosticQuery{})
			if err != nil || page.Fatal != "" {
				t.Fatal(page, err)
			}
			for _, item := range page.Items {
				if item.Fault != "" {
					t.Fatal("repair left an inconsistent row", item)
				}
			}
			if kind.IsTask() {
				replay, err := r.Enqueue(ctx, in, now.Add(5*time.Second))
				if err != nil || replay.Status != "cancelled" || replay.ID != j.ID {
					t.Fatal("repaired task resumed or changed identity", replay, err)
				}
				if kind == pluginjobs.RecordTask && (replay.Version != oldClaim.Version+1 || replay.LeaseToken != "") {
					t.Fatal("repair lost lease fencing")
				}
			} else {
				f, err := r.FreezeFanout(ctx, event, nil)
				if err != nil || !f.Cancelled || !f.Delivered || !reflect.DeepEqual(f.Subscriptions, subs) {
					t.Fatal("repair resumed fanout or changed cohort/completion", f, err)
				}
			}
		})
	}
}

func TestPostgreSQLPluginMaintenanceQuarantineIndexesCannotHideUnresolvedEvidence(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Now().UTC().Add(-time.Minute)
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "damaged", ActorID: 1, Payload: json.RawMessage(`{}`)}
	j, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_plugin_jobs SET plugin='wrong-reader' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	d := maintenanceDiagnostic(t, r, pluginjobs.RecordTask)
	isolated, err := r.QuarantineRecord(ctx, pluginjobs.QuarantineRequest{Reference: d.Reference, Type: d.Type, Key: "isolate", Operator: "operator", Reason: "index checked"}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err = pool.QueryRow(ctx, `SELECT data FROM np_plugin_record_quarantines WHERE id=$1`, isolated.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"repaired", "record_type", "reference", "captured_at", "data"} {
		t.Run(field, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `UPDATE np_plugin_record_quarantines SET record_type='task',reference=$2,captured_at=$3,repaired=false,data=$4 WHERE id=$1`, isolated.ID, isolated.Reference, isolated.CapturedAt, original); err != nil {
				t.Fatal(err)
			}
			var value any
			switch field {
			case "repaired":
				value = true
			case "record_type":
				value = "fanout"
			case "reference":
				value = strings.Repeat("0", 64)
			case "captured_at":
				value = now.Add(time.Hour)
			case "data":
				value = []byte("bad JSON")
			}
			if _, err := pool.Exec(ctx, `UPDATE np_plugin_record_quarantines SET `+field+`=$2 WHERE id=$1`, isolated.ID, value); err != nil {
				t.Fatal(err)
			}
			in.Key = "fresh-" + field
			if _, err := r.Enqueue(ctx, in, now); !errors.Is(err, errPluginQuarantineRow) {
				t.Fatal("bad quarantine index enabled admission", err)
			}
			if _, _, err := r.Claim(ctx, now); !errors.Is(err, errPluginQuarantineRow) {
				t.Fatal("bad quarantine index enabled claims", err)
			}
			page, err := r.InspectRecords(ctx, pluginjobs.DiagnosticQuery{})
			if err != nil || page.Fatal != "invalid_quarantine" {
				t.Fatal(page, err)
			}
			if err = r.CancelRestored(ctx, now.Add(time.Hour)); !errors.Is(err, errPluginQuarantineRow) {
				t.Fatal("restore hid corrupted quarantine evidence", err)
			}
		})
	}
}

func TestPostgreSQLPluginMaintenanceDuplicateTerminalReconciliation(t *testing.T) {
	for _, kind := range []pluginjobs.RecordType{pluginjobs.RecordTask, pluginjobs.RecordTaskHistory} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, pool, r := newPluginJobsIntegrationStore(t)
			now := time.Now().UTC().Add(-time.Minute)
			in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "duplicate", ActorID: 1, Payload: json.RawMessage(`{}`)}
			j, err := r.Enqueue(ctx, in, now)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Change(ctx, in.Plugin, j.ID, j.Version, "cancel", now); err != nil {
				t.Fatal(err)
			}
			current, err := scanTask(pool.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1`, j.ID))
			if err != nil {
				t.Fatal(err)
			}
			archived, err := pluginjobs.ArchiveJob(current, now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(archived)
			if _, err = pool.Exec(ctx, `INSERT INTO np_plugin_job_archives(id,plugin,status,updated_at,archived_at,data) VALUES($1,$2,$3,$4,$5,$6)`, j.ID, j.Plugin, archived.Status, archived.UpdatedAt, archived.ArchivedAt, raw); err != nil {
				t.Fatal(err)
			}
			d := maintenanceDiagnostic(t, r, kind)
			if d.Fault != "duplicate_identity" {
				t.Fatal(d)
			}
			isolated, err := r.QuarantineRecord(ctx, pluginjobs.QuarantineRequest{Reference: d.Reference, Type: kind, Key: "duplicate-isolation", Operator: "operator", Reason: "both terminal receipts checked"}, now.Add(2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			repair := pluginjobs.RepairRequest{ID: isolated.ID, Operator: "operator", Reason: "explicit surviving receipt"}
			if _, err = r.RepairRecord(ctx, repair, now.Add(3*time.Second)); !errors.Is(err, pluginjobs.ErrConflict) {
				t.Fatal("repair overwrote terminal survivor", err)
			}
			page, err := r.InspectRecords(ctx, pluginjobs.DiagnosticQuery{})
			if err != nil || len(page.Items) != 1 {
				t.Fatal(page, err)
			}
			repair.KeepReference = page.Items[0].Reference
			result, err := r.RepairRecord(ctx, repair, now.Add(3*time.Second))
			if err != nil || result.Repair == nil || result.Repair.Disposition != "reconciled_terminal" {
				t.Fatal(result, err)
			}
			if replay, err := r.Enqueue(ctx, in, now); err != nil || replay.Status != "cancelled" {
				t.Fatal("reconciliation lost permanent receipt", replay, err)
			}
		})
	}
}

func TestPostgreSQLPluginMaintenanceQuarantineAndRepairRollbackAndConcurrency(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Now().UTC().Add(-time.Minute)
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "transaction", ActorID: 1, Payload: json.RawMessage(`{}`)}
	j, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_plugin_jobs SET plugin='wrong-reader' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	d := maintenanceDiagnostic(t, r, pluginjobs.RecordTask)
	request := pluginjobs.QuarantineRequest{Reference: d.Reference, Type: d.Type, Key: "transaction-command", Operator: "operator", Reason: "checked index"}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_maintenance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'maintenance rollback fixture'; END $$;
CREATE TRIGGER fail_maintenance_delete BEFORE DELETE ON np_plugin_jobs FOR EACH ROW EXECUTE FUNCTION fail_maintenance()`); err != nil {
		t.Fatal(err)
	}
	if _, err = r.QuarantineRecord(ctx, request, now.Add(time.Second)); err == nil {
		t.Fatal("failed source deletion committed isolation")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_plugin_record_quarantines`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed isolation left evidence without an atomic move", count, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_maintenance_delete ON np_plugin_jobs`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			result, err := r.QuarantineRecord(ctx, request, now.Add(2*time.Second))
			if err != nil {
				t.Error(err)
				return
			}
			ids <- result.ID
		})
	}
	wg.Wait()
	close(ids)
	var id string
	for received := range ids {
		if id != "" && id != received {
			t.Fatal("one command generated multiple quarantine events")
		}
		id = received
	}
	if id == "" {
		t.Fatal("concurrent isolation did not succeed")
	}
	repair := pluginjobs.RepairRequest{ID: id, Operator: "operator", Reason: "JSON verified"}
	if _, err = pool.Exec(ctx, `CREATE TRIGGER fail_maintenance_insert BEFORE INSERT ON np_plugin_jobs FOR EACH ROW EXECUTE FUNCTION fail_maintenance()`); err != nil {
		t.Fatal(err)
	}
	if _, err = r.RepairRecord(ctx, repair, now.Add(3*time.Second)); err == nil {
		t.Fatal("failed replacement committed a repair audit")
	}
	var repaired bool
	if err = pool.QueryRow(ctx, `SELECT repaired FROM np_plugin_record_quarantines WHERE id=$1`, id).Scan(&repaired); err != nil || repaired {
		t.Fatal("repair audit survived rolled-back insertion", repaired, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_maintenance_insert ON np_plugin_jobs`); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		wg.Go(func() {
			if _, err := r.RepairRecord(ctx, repair, now.Add(4*time.Second)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	replay, err := r.Enqueue(ctx, in, now.Add(5*time.Second))
	if err != nil || replay.Status != "cancelled" || replay.Version != j.Version+1 {
		t.Fatal("concurrent repair repeated cancellation transition", replay, err)
	}
}

func TestPostgreSQLPluginMaintenanceUnprovableAndEmptyRecordFencesAllClaims(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			ctx, pool, r := newPluginJobsIntegrationStore(t)
			now := time.Now().UTC().Add(-time.Minute)
			in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "bad-identity", ActorID: 1, Payload: json.RawMessage(`{}`)}
			j, err := r.Enqueue(ctx, in, now)
			if err != nil {
				t.Fatal(err)
			}
			in.Key = "healthy"
			if _, err = r.Enqueue(ctx, in, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err = pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, j.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if empty {
				raw = []byte{}
			} else {
				raw = bytes.Replace(raw, []byte(`"id":"`+j.ID+`"`), []byte(`"id":"`+strings.Repeat("0", 64)+`"`), 1)
			}
			if _, err = pool.Exec(ctx, `UPDATE np_plugin_jobs SET data=$2 WHERE id=$1`, j.ID, raw); err != nil {
				t.Fatal(err)
			}
			d := maintenanceDiagnostic(t, r, pluginjobs.RecordTask)
			isolated, err := r.QuarantineRecord(ctx, pluginjobs.QuarantineRequest{Reference: d.Reference, Type: d.Type, Key: "isolate", Operator: "operator", Reason: "identity requires verified backup"}, now.Add(2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = r.Claim(ctx, now.Add(3*time.Second)); !errors.Is(err, pluginjobs.ErrQuarantined) {
				t.Fatal("unprovable identity enabled healthy claims", err)
			}
			if _, err = r.RepairRecord(ctx, pluginjobs.RepairRequest{ID: isolated.ID, Operator: "operator", Reason: "cannot guess identity"}, now.Add(3*time.Second)); err == nil {
				t.Fatal("unprovable original was repaired by guessing")
			}
			if err = r.CancelRestored(context.Background(), now.Add(time.Hour)); err != nil {
				t.Fatal("restore cancellation could not preserve isolated evidence", err)
			}
		})
	}
}

func TestPostgreSQLPluginMaintenanceEvidenceCountBudgetRollsBackMove(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Now().UTC().Add(-time.Minute)
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "capacity", ActorID: 1, Payload: json.RawMessage(`{}`)}
	j, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_plugin_jobs SET plugin='wrong-reader' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	values := make([][]any, 0, pluginjobs.MaxQuarantineRecords)
	for i := range pluginjobs.MaxQuarantineRecords {
		record := pluginjobs.StoredRecord{Type: pluginjobs.RecordFanout, Key: fmt.Sprint(i), Data: []byte{}, Metadata: []byte(`{}`)}
		q, err := pluginjobs.CaptureQuarantine(record, "invalid_data", pluginjobs.QuarantineRequest{Reference: pluginjobs.RecordReference(record), Type: record.Type, Key: fmt.Sprint(i), Operator: "fixture", Reason: "capacity evidence"}, now)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(q)
		values = append(values, []any{q.ID, string(q.Original.Type), q.Reference, q.CapturedAt, false, raw})
	}
	if _, err = pool.CopyFrom(ctx, pgx.Identifier{"np_plugin_record_quarantines"}, []string{"id", "record_type", "reference", "captured_at", "repaired", "data"}, pgx.CopyFromRows(values)); err != nil {
		t.Fatal(err)
	}
	d := maintenanceDiagnostic(t, r, pluginjobs.RecordTask)
	if _, err = r.QuarantineRecord(ctx, pluginjobs.QuarantineRequest{Reference: d.Reference, Type: d.Type, Key: "over-capacity", Operator: "operator", Reason: "index checked"}, now.Add(time.Second)); !errors.Is(err, pluginjobs.ErrCapacity) {
		t.Fatal("isolation exceeded permanent evidence count budget", err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_plugin_record_quarantines`).Scan(&count); err != nil || count != pluginjobs.MaxQuarantineRecords {
		t.Fatal("capacity failure partially moved evidence", count, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_plugin_jobs WHERE id=$1`, j.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("capacity failure deleted original row", count, err)
	}
}

func TestPostgreSQLPluginMaintenanceKnownTaskAliasFencesClaimsAndLetsHealthyWorkProceed(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Now().UTC().Add(-time.Minute)
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "alias-task", ActorID: 1, Payload: json.RawMessage(`{}`)}
	j, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, _, err := r.Claim(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	healthy := in
	healthy.Key = "healthy-work"
	healthyJob, err := r.Enqueue(ctx, healthy, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(claim)
	alias := strings.Repeat("0", 64)
	if _, err = pool.Exec(ctx, `INSERT INTO np_plugin_jobs(id,plugin,status,available_at,updated_at,lease_until,data) VALUES($1,$2,$3,$4,$5,$6,$7)`, alias, j.Plugin, claim.Status, claim.AvailableAt, claim.UpdatedAt, claim.LeaseUntil, raw); err != nil {
		t.Fatal(err)
	}
	page, err := r.InspectRecords(ctx, pluginjobs.DiagnosticQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var reference string
	for _, item := range page.Items {
		if item.TaskID == j.ID && item.Fault == "duplicate_identity" && item.IndexMismatch {
			reference = item.Reference
		}
	}
	if reference == "" {
		t.Fatal("diagnostic could not distinguish incorrectly indexed duplicate")
	}
	isolated, err := r.QuarantineRecord(ctx, pluginjobs.QuarantineRequest{Reference: reference, Type: pluginjobs.RecordTask, Key: "isolate-alias", Operator: "operator", Reason: "duplicate SQL key checked"}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = r.CheckClaim(ctx, claim, now.Add(2*time.Second)); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("known alias allowed old claim to run", err)
	}
	if err = r.Finish(ctx, claim, "", now.Add(2*time.Second)); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatal("known alias permitted completion", err)
	}
	healthyClaim, found, err := r.Claim(ctx, now.Add(pluginjobs.LeaseDuration))
	if err != nil || !found || healthyClaim.ID != healthyJob.ID {
		t.Fatal("known identity blocked healthy work or reclaimed its duplicate", healthyClaim.ID, found, err)
	}
	if err = r.Finish(ctx, healthyClaim, "", now.Add(pluginjobs.LeaseDuration+time.Second)); err != nil {
		t.Fatal("healthy claim could not finish while another identity was isolated", err)
	}
	if err = r.Change(ctx, j.Plugin, j.ID, claim.Version, "cancel", now.Add(pluginjobs.LeaseDuration+time.Second)); err != nil {
		t.Fatal(err)
	}
	page, err = r.InspectRecords(ctx, pluginjobs.DiagnosticQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.TaskID == j.ID && item.Task != nil && item.Task.Status == "cancelled" {
			reference = item.Reference
		}
	}
	if _, err = r.RepairRecord(ctx, pluginjobs.RepairRequest{ID: isolated.ID, Operator: "operator", Reason: "explicit terminal survivor checked", KeepReference: reference}, now.Add(pluginjobs.LeaseDuration+2*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQLPluginMaintenanceIncompleteFanoutAliasFencesDelivery(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Now().UTC().Add(-time.Minute)
	event := pluginjobs.Event{Key: "frozen-original", Kind: "content.updated", ObjectID: 1, Version: 2, At: now}
	f, err := r.FreezeFanout(ctx, event, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(f)
	if _, err = pool.Exec(ctx, `INSERT INTO np_plugin_fanouts(event_key,data) VALUES('wrong-frozen-key',$1)`, raw); err != nil {
		t.Fatal(err)
	}
	page, err := r.InspectRecords(ctx, pluginjobs.DiagnosticQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var reference string
	for _, item := range page.Items {
		if item.Fault == "duplicate_identity" && item.IndexMismatch {
			reference = item.Reference
		}
	}
	isolated, err := r.QuarantineRecord(ctx, pluginjobs.QuarantineRequest{Reference: reference, Type: pluginjobs.RecordFanout, Key: "isolate-frozen-alias", Operator: "operator", Reason: "frozen receipt alias checked"}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.FindFanout(ctx, event); !errors.Is(err, pluginjobs.ErrQuarantined) {
		t.Fatal("incomplete frozen lookup bypassed alias isolation", err)
	}
	if _, err = r.FreezeFanout(ctx, event, nil); !errors.Is(err, pluginjobs.ErrQuarantined) {
		t.Fatal("incomplete frozen replay bypassed alias isolation", err)
	}
	if err = r.CompleteFanout(ctx, event); !errors.Is(err, pluginjobs.ErrQuarantined) {
		t.Fatal("quarantined receipt was marked delivered", err)
	}
	if err = r.CancelRestored(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if confirmed, err := r.FreezeFanout(ctx, event, nil); err != nil || !confirmed.Cancelled || confirmed.Delivered {
		t.Fatal("cancelled receipt confirmation lost frozen facts", confirmed, err)
	}
	page, err = r.InspectRecords(ctx, pluginjobs.DiagnosticQuery{})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	if _, err = r.RepairRecord(ctx, pluginjobs.RepairRequest{ID: isolated.ID, Operator: "operator", Reason: "explicit cancelled frozen survivor", KeepReference: page.Items[0].Reference}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
}
