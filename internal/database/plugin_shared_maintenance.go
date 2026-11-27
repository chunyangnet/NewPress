package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/pluginshared"
)

var _ pluginshared.Store = (*PluginJobRepository)(nil)
var errSharedQuarantine = errors.New("shared plugin quarantine evidence is inconsistent")

// Includes repaired rows; indexed booleans never hide evidence from validation.
func loadSharedQuarantines(ctx context.Context, tx pgx.Tx) ([]pluginshared.Quarantine, error) {
	var count, total, largest int64
	if err := tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(data)),0),coalesce(max(octet_length(data)),0) FROM np_plugin_shared_quarantines`).Scan(&count, &total, &largest); err != nil {
		return nil, err
	}
	if count > pluginshared.MaxQuarantines || total > pluginshared.MaxQuarantineBytes || largest > pluginshared.MaxQuarantineRecordBytes {
		return nil, errSharedQuarantine
	}
	rows, err := tx.Query(ctx, `SELECT id,record_type,reference,captured_at,repaired,data FROM np_plugin_shared_quarantines ORDER BY id LIMIT $1`, pluginshared.MaxQuarantines+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []pluginshared.Quarantine{}
	for rows.Next() {
		var id, kind, reference string
		var at time.Time
		var repaired bool
		var raw []byte
		if err = rows.Scan(&id, &kind, &reference, &at, &repaired, &raw); err != nil {
			return nil, err
		}
		q, e := packagefs.DecodeJSON[pluginshared.Quarantine](bytes.NewReader(raw), pluginshared.MaxQuarantineRecordBytes, "shared quarantine")
		if e != nil || pluginshared.ValidateQuarantine(q) != nil || q.ID != id || q.Original.Type != kind || q.Reference != reference || !q.CapturedAt.Truncate(time.Microsecond).Equal(at) || repaired != (q.Repair != nil) {
			return nil, errSharedQuarantine
		}
		if q.Repair == nil {
			total += pluginshared.RepairReserve
		}
		if total > pluginshared.MaxQuarantineBytes || len(result) >= pluginshared.MaxQuarantines {
			return nil, errSharedQuarantine
		}
		result = append(result, q)
	}
	return result, rows.Err()
}

func sharedQuarantineGate(ctx context.Context, tx pgx.Tx) error {
	records, err := loadSharedQuarantines(ctx, tx)
	if err != nil {
		return err
	}
	for _, q := range records {
		if q.Repair == nil {
			return pluginshared.ErrQuarantined
		}
	}
	return nil
}

func readSharedRecords(ctx context.Context, tx pgx.Tx) ([]pluginshared.Record, error) {
	result := []pluginshared.Record{}
	for _, kind := range []string{pluginshared.Authority, pluginshared.ContentEffect, pluginshared.WebhookIntent} {
		table, columns, maxCount, maxBytes := "np_plugin_task_authorizations", pluginTaskControlColumns, plugin.MaxTaskControls, plugin.MaxTaskControlTotal
		if kind == pluginshared.ContentEffect {
			table, columns, maxCount, maxBytes = "np_plugin_content_effects", "id,job_id,slot,data", plugineffects.MaxReceipts, plugineffects.MaxTotalBytes
		} else if kind == pluginshared.WebhookIntent {
			table, columns, maxCount, maxBytes = "np_plugin_webhook_intents", "id,job_id,slot,version,state,data", plugindispatch.MaxRecords, plugindispatch.MaxTotalBytes
		}
		var count, size, largest, metadata int64
		indexBytes := "octet_length(slug)+octet_length(epoch)+32"
		if kind == pluginshared.ContentEffect {
			indexBytes = "octet_length(id)+octet_length(job_id)+octet_length(slot)"
		} else if kind == pluginshared.WebhookIntent {
			indexBytes = "octet_length(id)+octet_length(job_id)+octet_length(slot)+octet_length(state)+8"
		}
		if err := tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(data)),0),coalesce(max(octet_length(data)),0),coalesce(max(`+indexBytes+`),0) FROM `+table).Scan(&count, &size, &largest, &metadata); err != nil {
			return nil, err
		}
		if count > int64(maxCount) || size > int64(maxBytes) || largest > pluginshared.MaxRawBytes || metadata > 4096 {
			return nil, pluginshared.ErrCapacity
		}
		rows, err := tx.Query(ctx, `SELECT `+columns+` FROM `+table+` ORDER BY 1 LIMIT $1`, maxCount+1)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r := pluginshared.Record{Type: kind}
			i := pluginshared.Index{}
			if kind == pluginshared.Authority {
				err = rows.Scan(&r.Key, &i.Revision, &i.Epoch, &i.Enabled, &i.Retired, &r.Data)
			} else if kind == pluginshared.WebhookIntent {
				err = rows.Scan(&r.Key, &i.JobID, &i.Slot, &i.Version, &i.State, &r.Data)
			} else {
				err = rows.Scan(&r.Key, &i.JobID, &i.Slot, &r.Data)
			}
			if err != nil {
				rows.Close()
				return nil, err
			}
			r.Index, err = json.Marshal(i)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if len(r.Key) > 4096 || len(r.Index) > 4096 || len(r.Data) > pluginshared.MaxRawBytes {
				rows.Close()
				return nil, pluginshared.ErrCapacity
			}
			result = append(result, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (r *PluginJobRepository) InspectSharedRecords(ctx context.Context, q pluginshared.Query) (pluginshared.Page, error) {
	q, err := pluginshared.NormalizeQuery(q)
	if err != nil {
		return pluginshared.Page{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return pluginshared.Page{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_task_authorizations,np_plugin_content_effects,np_plugin_shared_quarantines,np_plugin_webhook_intents IN ACCESS SHARE MODE`); err != nil {
		return pluginshared.Page{}, err
	}
	quarantines, err := loadSharedQuarantines(ctx, tx)
	if err != nil {
		if errors.Is(err, errSharedQuarantine) {
			return pluginshared.Page{Items: []pluginshared.Diagnostic{}, NextOffset: -1, Fatal: "invalid_quarantine"}, nil
		}
		return pluginshared.Page{}, err
	}
	records, err := readSharedRecords(ctx, tx)
	if errors.Is(err, pluginshared.ErrCapacity) {
		return pluginshared.Page{Items: []pluginshared.Diagnostic{}, NextOffset: -1, Fatal: "storage_capacity"}, nil
	}
	if err != nil {
		return pluginshared.Page{}, err
	}
	page := pluginshared.PaginateDiagnostics(pluginshared.DiagnoseWithQuarantines(records, quarantines), q)
	return page, tx.Commit(ctx)
}
func (r *PluginJobRepository) ListSharedQuarantines(ctx context.Context, q pluginshared.Query) (pluginshared.QuarantinePage, error) {
	q, err := pluginshared.NormalizeQuery(q)
	if err != nil {
		return pluginshared.QuarantinePage{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return pluginshared.QuarantinePage{}, err
	}
	defer tx.Rollback(context.Background())
	records, err := loadSharedQuarantines(ctx, tx)
	if err != nil {
		return pluginshared.QuarantinePage{}, err
	}
	page := pluginshared.PaginateQuarantines(records, q)
	return page, tx.Commit(ctx)
}

// Same advisory/table ordering as restore. The task table fences claims and
// effect commits; authority and receipt tables then fence their writers.
func lockSharedMaintenance(ctx context.Context, tx pgx.Tx) error {
	for _, key := range []string{"newpress-pluginjobs-admission", "newpress-plugin-fanout-admission", "newpress-plugin-task-control-admission"} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `LOCK TABLE np_plugin_fanout_archives,np_plugin_fanouts,np_plugin_job_archives,np_plugin_jobs,np_plugin_record_quarantines,np_plugin_task_authorizations,np_plugin_content_effects,np_plugin_shared_quarantines,np_plugin_webhook_intents IN EXCLUSIVE MODE`)
	return err
}
func saveSharedQuarantine(ctx context.Context, tx pgx.Tx, q pluginshared.Quarantine, insert bool) error {
	if err := pluginshared.ValidateQuarantine(q); err != nil {
		return err
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return err
	}
	if insert {
		_, err = tx.Exec(ctx, `INSERT INTO np_plugin_shared_quarantines(id,record_type,reference,captured_at,repaired,data) VALUES($1,$2,$3,$4,$5,$6)`, q.ID, q.Original.Type, q.Reference, q.CapturedAt, q.Repair != nil, raw)
	} else {
		_, err = tx.Exec(ctx, `UPDATE np_plugin_shared_quarantines SET repaired=$2,data=$3 WHERE id=$1`, q.ID, q.Repair != nil, raw)
	}
	if err != nil {
		return err
	}
	_, err = loadSharedQuarantines(ctx, tx)
	return err
}
func (r *PluginJobRepository) QuarantineSharedRecord(ctx context.Context, request pluginshared.CaptureRequest, now time.Time) (pluginshared.QuarantineSummary, error) {
	if pluginshared.ValidateCapture(request) != nil || now.IsZero() {
		return pluginshared.QuarantineSummary{}, pluginshared.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return pluginshared.QuarantineSummary{}, err
	}
	defer tx.Rollback(context.Background())
	if err = lockSharedMaintenance(ctx, tx); err != nil {
		return pluginshared.QuarantineSummary{}, err
	}
	quarantines, err := loadSharedQuarantines(ctx, tx)
	if err != nil {
		return pluginshared.QuarantineSummary{}, err
	}
	for _, old := range quarantines {
		if old.ID == pluginshared.CaptureID(request) {
			if err = pluginshared.MatchCapture(old, request); err != nil {
				return pluginshared.QuarantineSummary{}, err
			}
			return pluginshared.Summarize(old), tx.Commit(ctx)
		}
	}
	records, err := readSharedRecords(ctx, tx)
	if err != nil {
		return pluginshared.QuarantineSummary{}, err
	}
	for i, d := range pluginshared.DiagnoseWithQuarantines(records, quarantines) {
		if d.Type != request.Type || d.Reference != request.Reference {
			continue
		}
		if d.Fault == "" {
			return pluginshared.QuarantineSummary{}, pluginshared.ErrInvalid
		}
		q, err := pluginshared.Capture(records[i], d.Fault, request, now)
		if err != nil {
			return pluginshared.QuarantineSummary{}, err
		}
		if err = saveSharedQuarantine(ctx, tx, q, true); err != nil {
			return pluginshared.QuarantineSummary{}, err
		}
		statement := `DELETE FROM np_plugin_task_authorizations WHERE slug=$1`
		if request.Type == pluginshared.ContentEffect {
			statement = `DELETE FROM np_plugin_content_effects WHERE id=$1`
		} else if request.Type == pluginshared.WebhookIntent {
			statement = `DELETE FROM np_plugin_webhook_intents WHERE id=$1`
		}
		if _, err = tx.Exec(ctx, statement, q.Original.Key); err != nil {
			return pluginshared.QuarantineSummary{}, err
		}
		return pluginshared.Summarize(q), tx.Commit(ctx)
	}
	return pluginshared.QuarantineSummary{}, pluginshared.ErrConflict
}

func insertSharedRepair(ctx context.Context, tx pgx.Tx, r pluginshared.Record) error {
	if pluginshared.Diagnose(r).Fault != "" {
		return pluginshared.ErrInvalid
	}
	i, err := packagefs.DecodeJSON[pluginshared.Index](bytes.NewReader(r.Index), 4096, "shared repair index")
	if err != nil {
		return err
	}
	if r.Type == pluginshared.Authority {
		_, err = tx.Exec(ctx, `INSERT INTO np_plugin_task_authorizations(slug,revision,epoch,enabled,retired,data) VALUES($1,$2,$3,$4,$5,$6)`, r.Key, i.Revision, i.Epoch, i.Enabled, i.Retired, r.Data)
	} else if r.Type == pluginshared.WebhookIntent {
		_, err = tx.Exec(ctx, `INSERT INTO np_plugin_webhook_intents(id,job_id,slot,version,state,data) VALUES($1,$2,$3,$4,$5,$6)`, r.Key, i.JobID, i.Slot, i.Version, i.State, r.Data)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO np_plugin_content_effects(id,job_id,slot,data) VALUES($1,$2,$3,$4)`, r.Key, i.JobID, i.Slot, r.Data)
	}
	return err
}

// An isolated receipt may represent an already committed write. Repair restores
// that same result; authorization repairs always retire and rotate consent.
func (r *PluginJobRepository) RepairSharedRecord(ctx context.Context, request pluginshared.RepairRequest, now time.Time) (pluginshared.QuarantineSummary, error) {
	if pluginshared.ValidateRepair(request) != nil || now.IsZero() {
		return pluginshared.QuarantineSummary{}, pluginshared.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return pluginshared.QuarantineSummary{}, err
	}
	defer tx.Rollback(context.Background())
	if err = lockSharedMaintenance(ctx, tx); err != nil {
		return pluginshared.QuarantineSummary{}, err
	}
	quarantines, err := loadSharedQuarantines(ctx, tx)
	if err != nil {
		return pluginshared.QuarantineSummary{}, err
	}
	var q pluginshared.Quarantine
	for _, candidate := range quarantines {
		if candidate.ID == request.ID {
			q = candidate
			break
		}
	}
	if q.ID == "" {
		return pluginshared.QuarantineSummary{}, pluginshared.ErrConflict
	}
	if q.Repair != nil {
		if err = pluginshared.MatchRepair(q, request); err != nil {
			return pluginshared.QuarantineSummary{}, err
		}
		return pluginshared.Summarize(q), tx.Commit(ctx)
	}
	records, err := readSharedRecords(ctx, tx)
	if err != nil {
		return pluginshared.QuarantineSummary{}, err
	}
	var audit pluginshared.RepairAudit
	var repaired pluginshared.Record
	if request.KeepReference != "" {
		var kept *pluginshared.Record
		others := []pluginshared.Record{}
		for i, r := range records {
			if pluginshared.Reference(r) == request.KeepReference {
				kept = &records[i]
			} else {
				others = append(others, r)
			}
		}
		if kept == nil {
			return pluginshared.QuarantineSummary{}, pluginshared.ErrConflict
		}
		if err = pluginshared.ValidateRepairTarget(*kept, others); err != nil {
			return pluginshared.QuarantineSummary{}, err
		}
		audit, err = pluginshared.Reconcile(q, request, *kept, now)
		repaired = *kept
	} else {
		var out pluginshared.Record
		out, audit, err = pluginshared.BuildRepair(q, request, now)
		if err == nil {
			err = pluginshared.ValidateRepairTarget(out, records)
		}
		if err == nil {
			err = insertSharedRepair(ctx, tx, out)
		}
		repaired = out
	}
	if err != nil {
		return pluginshared.QuarantineSummary{}, err
	}
	if repaired.Type == pluginshared.ContentEffect || repaired.Type == pluginshared.WebhookIntent {
		if err = cancelRepairedEffectTask(ctx, tx, pluginshared.Diagnose(repaired).JobID, now); err != nil {
			return pluginshared.QuarantineSummary{}, err
		}
	}
	q.Repair = &audit
	if err = saveSharedQuarantine(ctx, tx, q, false); err != nil {
		return pluginshared.QuarantineSummary{}, err
	}
	// Do not demand that unrelated damaged records are repaired simultaneously;
	// preserve them and use conservative reservation while checking budgets.
	if err = checkSharedRepairBudgets(ctx, tx); err != nil {
		return pluginshared.QuarantineSummary{}, err
	}
	return pluginshared.Summarize(q), tx.Commit(ctx)
}

func cancelRepairedEffectTask(ctx context.Context, tx pgx.Tx, id string, now time.Time) error {
	job, err := scanTask(tx.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if job.Status == "succeeded" || job.Status == "cancelled" {
		return nil
	}
	q, err := taskQueue(&job)
	if err != nil {
		return err
	}
	if err = q.CancelRestored(ctx, now); err != nil {
		return err
	}
	state, err := q.Backend.Read(ctx)
	if err != nil {
		return err
	}
	if state.Jobs[id].Status != "cancelled" {
		return pluginjobs.ErrConflict
	}
	return saveTask(ctx, tx, state.Jobs[id])
}

func checkSharedRepairBudgets(ctx context.Context, tx pgx.Tx) error {
	records, err := readSharedRecords(ctx, tx)
	if err != nil {
		return err
	}
	reserved, webhookReserved := 0, 0
	perJob := map[string]int{}
	for _, r := range records {
		if r.Type == pluginshared.Authority {
			c, err := packagefs.DecodeJSON[plugin.TaskControl](bytes.NewReader(r.Data), plugin.MaxTaskControlBytes, "authority budget")
			b, e := plugin.TaskControlBudgetBytes(c)
			if err != nil || e != nil {
				b = plugin.MaxTaskControlBytes
			}
			reserved += b
		} else if r.Type == pluginshared.WebhookIntent {
			i, err := packagefs.DecodeJSON[plugindispatch.Intent](bytes.NewReader(r.Data), plugindispatch.MaxRecordBytes, "webhook budget")
			budget := plugindispatch.MaxRecordBytes
			if err == nil && plugindispatch.Validate(i) == nil {
				budget = plugindispatch.BudgetBytes(i)
				perJob["webhook:"+i.JobID]++
				if perJob["webhook:"+i.JobID] > plugindispatch.MaxSlotsPerTask {
					return pluginshared.ErrCapacity
				}
			}
			webhookReserved += budget
		} else {
			d := pluginshared.Diagnose(r)
			if d.JobID != "" {
				perJob["content:"+d.JobID]++
				if perJob["content:"+d.JobID] > plugineffects.MaxSlotsPerTask {
					return pluginshared.ErrCapacity
				}
			}
		}
	}
	if reserved > plugin.MaxTaskControlTotal || webhookReserved > plugindispatch.MaxTotalBytes {
		return pluginshared.ErrCapacity
	}
	return nil
}
