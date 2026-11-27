package database

import (
	"context"
	"encoding/json"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

// CancelRestored must run with application workers stopped, before reopening a
// restored database to traffic. It fences old claims and preserves deduplication
// history atomically. It does not undo side effects completed before restoration.
func (r *PluginJobRepository) CancelRestored(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return pluginjobs.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	// Share archive/admission ordering before taking all tables. Otherwise an
	// archive holding a task row could wait for a fanout table held by restore.
	// The caller must still quiesce workers executing guest code.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-pluginjobs-admission',0))`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-plugin-fanout-admission',0))`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-plugin-task-control-admission',0))`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_fanout_archives, np_plugin_fanouts, np_plugin_job_archives, np_plugin_jobs, np_plugin_record_quarantines, np_plugin_task_authorizations, np_plugin_content_effects, np_plugin_shared_quarantines, np_plugin_webhook_intents IN EXCLUSIVE MODE`); err != nil {
		return err
	}
	if _, err = loadSharedQuarantines(ctx, tx); err != nil {
		return err
	}
	if _, err = loadPGPluginQuarantines(ctx, tx); err != nil {
		return err
	}
	// Internal write receipts are permanent facts. Validate and retain their
	// original bytes while retiring authority and cancelling restored work.
	if _, _, err = loadPluginContentEffects(ctx, tx); err != nil {
		return err
	}
	var duplicate bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_plugin_jobs JOIN np_plugin_job_archives USING(id))
 OR EXISTS(SELECT 1 FROM np_plugin_fanouts JOIN np_plugin_fanout_archives USING(event_key))
 OR EXISTS(SELECT 1 FROM np_plugin_jobs JOIN np_plugin_job_archives USING(reissue_id))`).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return errPluginTaskRow
	}
	rows, err := tx.Query(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs ORDER BY id`)
	if err != nil {
		return err
	}
	jobs := []pluginjobs.Job{}
	for rows.Next() {
		j, e := scanTask(rows)
		if e != nil {
			rows.Close()
			return e
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		q, e := taskQueue(&j)
		if e != nil {
			return e
		}
		if e = q.CancelRestored(ctx, now); e != nil {
			return e
		}
		state, e := q.Backend.Read(ctx)
		if e != nil {
			return e
		}
		next := state.Jobs[j.ID]
		if next.Version != j.Version {
			if e = saveTask(ctx, tx, next); e != nil {
				return e
			}
		}
	}
	rows, err = tx.Query(ctx, `SELECT event_key,data FROM np_plugin_fanouts ORDER BY event_key`)
	if err != nil {
		return err
	}
	receipts := []pluginjobs.FanoutReceipt{}
	for rows.Next() {
		var key string
		var raw []byte
		if err = rows.Scan(&key, &raw); err != nil {
			rows.Close()
			return err
		}
		receipt, e := decodeFanout(key, raw)
		if e != nil {
			rows.Close()
			return e
		}
		receipts = append(receipts, receipt)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		receipt.Cancelled = true
		raw, e := json.Marshal(receipt)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE np_plugin_fanouts SET data=$2 WHERE event_key=$1`, receipt.Event.Key, raw); e != nil {
			return e
		}
	}
	// Even immutable task history must be validated before reopening a restored
	// database; it is the authority for old enqueue keys.
	rows, err = tx.Query(ctx, `SELECT `+archivedTaskColumns+` FROM np_plugin_job_archives ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		if _, err = scanArchivedTask(rows); err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT event_key,archived_at,cancelled,data FROM np_plugin_fanout_archives ORDER BY event_key`)
	if err != nil {
		return err
	}
	archived := []pluginjobs.ArchivedFanout{}
	for rows.Next() {
		f, e := scanArchivedFanout(rows)
		if e != nil {
			rows.Close()
			return e
		}
		archived = append(archived, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, f := range archived {
		if f.Receipt.Cancelled {
			continue
		}
		f.Receipt.Cancelled = true
		raw, e := json.Marshal(f)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE np_plugin_fanout_archives SET cancelled=true,data=$2 WHERE event_key=$1`, f.Receipt.Event.Key, raw); e != nil {
			return e
		}
	}
	if err = suspendRestoredTaskControls(ctx, tx); err != nil {
		return err
	}
	if err = cancelRestoredWebhooks(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
