package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/pluginjobs"
)

var _ pluginjobs.ArchiveStore = (*PluginJobRepository)(nil)

const archivedTaskColumns = "id,plugin,status,updated_at,archived_at,data,reissue_id"

func scanArchivedTask(row interface{ Scan(...any) error }) (pluginjobs.ArchivedJob, error) {
	var id, plugin, status string
	var updatedAt, archivedAt time.Time
	var raw []byte
	var reissueID *string
	if err := row.Scan(&id, &plugin, &status, &updatedAt, &archivedAt, &raw, &reissueID); err != nil {
		return pluginjobs.ArchivedJob{}, err
	}
	j, err := decodeArchivedTask(raw)
	if err != nil {
		return pluginjobs.ArchivedJob{}, err
	}
	if err = validateArchivedTaskIndex(j, id, plugin, status, updatedAt, archivedAt, reissueID); err != nil {
		return pluginjobs.ArchivedJob{}, err
	}
	return j, nil
}

func decodeArchivedTask(raw []byte) (pluginjobs.ArchivedJob, error) {
	j, err := packagefs.DecodeJSON[pluginjobs.ArchivedJob](bytes.NewReader(raw), 4096, "archived plugin task")
	if err != nil {
		return pluginjobs.ArchivedJob{}, err
	}
	return j, pluginjobs.ValidateArchivedJob(j)
}

func validateArchivedTaskIndex(j pluginjobs.ArchivedJob, id, plugin, status string, updatedAt, archivedAt time.Time, reissueID *string) error {
	if id != j.ID || plugin != j.Plugin || status != j.Status || !updatedAt.Equal(j.UpdatedAt.Truncate(time.Microsecond)) ||
		!archivedAt.Equal(j.ArchivedAt.Truncate(time.Microsecond)) || !sameStoredReissueID(reissueID, j.Review) {
		return errPluginTaskRow
	}
	return nil
}

func decodeFanout(key string, raw []byte) (pluginjobs.FanoutReceipt, error) {
	f, err := packagefs.DecodeJSON[pluginjobs.FanoutReceipt](bytes.NewReader(raw), 524288, "plugin fanout receipt")
	if err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	if err = pluginjobs.ValidateFanout(f); err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	if key != f.Event.Key {
		return pluginjobs.FanoutReceipt{}, pluginjobs.ErrInvalid
	}
	return f, nil
}

func scanArchivedFanout(row interface{ Scan(...any) error }) (pluginjobs.ArchivedFanout, error) {
	var key string
	var archivedAt time.Time
	var cancelled bool
	var raw []byte
	if err := row.Scan(&key, &archivedAt, &cancelled, &raw); err != nil {
		return pluginjobs.ArchivedFanout{}, err
	}
	f, err := packagefs.DecodeJSON[pluginjobs.ArchivedFanout](bytes.NewReader(raw), 524288, "archived plugin fanout")
	if err != nil {
		return pluginjobs.ArchivedFanout{}, err
	}
	if err = pluginjobs.ValidateArchivedFanout(f); err != nil {
		return pluginjobs.ArchivedFanout{}, err
	}
	if key != f.Receipt.Event.Key || cancelled != f.Receipt.Cancelled || !archivedAt.Equal(f.ArchivedAt.Truncate(time.Microsecond)) {
		return pluginjobs.ArchivedFanout{}, pluginjobs.ErrInvalid
	}
	return f, nil
}

type fanoutQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// A single statement sees either side of the move, including during concurrent
// archive commits. Finding both sides indicates damaged or externally inserted
// history, rather than permission to silently choose one receipt.
func findStoredFanout(ctx context.Context, db fanoutQuerier, event pluginjobs.Event) (pluginjobs.FanoutReceipt, bool, error) {
	rows, err := db.Query(ctx, `SELECT event_key,NULL::timestamptz,NULL::boolean,data FROM np_plugin_fanouts WHERE event_key=$1
 UNION ALL SELECT event_key,archived_at,cancelled,data FROM np_plugin_fanout_archives WHERE event_key=$1`, event.Key)
	if err != nil {
		return pluginjobs.FanoutReceipt{}, false, err
	}
	defer rows.Close()
	var found bool
	var out pluginjobs.FanoutReceipt
	for rows.Next() {
		if found {
			return pluginjobs.FanoutReceipt{}, false, pluginjobs.ErrInvalid
		}
		var key string
		var archivedAt *time.Time
		var cancelled *bool
		var raw []byte
		if err = rows.Scan(&key, &archivedAt, &cancelled, &raw); err != nil {
			return pluginjobs.FanoutReceipt{}, false, err
		}
		if archivedAt == nil {
			out, err = decodeFanout(key, raw)
		} else {
			var archived pluginjobs.ArchivedFanout
			archived, err = packagefs.DecodeJSON[pluginjobs.ArchivedFanout](bytes.NewReader(raw), 524288, "archived plugin fanout")
			if err == nil {
				err = pluginjobs.ValidateArchivedFanout(archived)
			}
			if err == nil && (key != archived.Receipt.Event.Key || cancelled == nil || *cancelled != archived.Receipt.Cancelled || !archivedAt.Equal(archived.ArchivedAt.Truncate(time.Microsecond))) {
				err = pluginjobs.ErrInvalid
			}
			out = archived.Receipt
		}
		if err != nil {
			return pluginjobs.FanoutReceipt{}, false, err
		}
		if !pluginjobs.SameEvent(out.Event, event) {
			return pluginjobs.FanoutReceipt{}, false, pluginjobs.ErrConflict
		}
		found = true
	}
	if err = rows.Err(); err != nil {
		return pluginjobs.FanoutReceipt{}, false, err
	}
	return out, found, nil
}

func (r *PluginJobRepository) Archive(ctx context.Context, query pluginjobs.ArchiveQuery, now time.Time) (pluginjobs.ArchiveResult, error) {
	query, err := pluginjobs.NormalizeArchiveQuery(query, now)
	if err != nil {
		return pluginjobs.ArchiveResult{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return pluginjobs.ArchiveResult{}, err
	}
	defer tx.Rollback(context.Background())
	// All archive writers share the task admission lock for the history budget.
	// Fanout admission is taken second; ordinary claims remain row transactions.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-pluginjobs-admission',0))`); err != nil {
		return pluginjobs.ArchiveResult{}, err
	}
	if query.Fanouts {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-plugin-fanout-admission',0))`); err != nil {
			return pluginjobs.ArchiveResult{}, err
		}
	}
	if _, err = loadPGPluginQuarantines(ctx, tx); err != nil {
		return pluginjobs.ArchiveResult{}, err
	}
	jobs := []pluginjobs.Job{}
	if query.Tasks {
		rows, e := tx.Query(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs
 WHERE ($1='' OR plugin=$1) AND status IN ('succeeded','cancelled') AND updated_at <= $2
 ORDER BY updated_at,id FOR UPDATE`, query.Plugin, query.Before)
		if e != nil {
			return pluginjobs.ArchiveResult{}, e
		}
		for rows.Next() {
			j, e := scanTask(rows)
			if e != nil {
				rows.Close()
				return pluginjobs.ArchiveResult{}, e
			}
			// SQL rounds down nanoseconds; preserve the exact domain cutoff.
			if !j.UpdatedAt.After(query.Before) {
				jobs = append(jobs, j)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return pluginjobs.ArchiveResult{}, e
		}
		sort.Slice(jobs, func(i, j int) bool {
			if jobs[i].UpdatedAt.Equal(jobs[j].UpdatedAt) {
				return jobs[i].ID < jobs[j].ID
			}
			return jobs[i].UpdatedAt.Before(jobs[j].UpdatedAt)
		})
		jobs = jobs[:min(len(jobs), query.Limit)]
	}
	fanouts := []pluginjobs.FanoutReceipt{}
	if query.Fanouts {
		// The active table is capped at 10,000 receipts. Validate before moving
		// bytea records; parsing unchecked bytes in SQL could bypass validation.
		rows, e := tx.Query(ctx, `SELECT event_key,data FROM np_plugin_fanouts ORDER BY event_key FOR UPDATE`)
		if e != nil {
			return pluginjobs.ArchiveResult{}, e
		}
		for rows.Next() {
			var key string
			var raw []byte
			if e = rows.Scan(&key, &raw); e != nil {
				rows.Close()
				return pluginjobs.ArchiveResult{}, e
			}
			f, e := decodeFanout(key, raw)
			if e != nil {
				rows.Close()
				return pluginjobs.ArchiveResult{}, e
			}
			if (f.Delivered || f.Cancelled) && !f.Event.At.After(query.Before) {
				fanouts = append(fanouts, f)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return pluginjobs.ArchiveResult{}, e
		}
		sort.Slice(fanouts, func(i, j int) bool {
			a, b := fanouts[i].Event, fanouts[j].Event
			if a.At.Equal(b.At) {
				return a.Key < b.Key
			}
			return a.At.Before(b.At)
		})
		fanouts = fanouts[:min(len(fanouts), query.Limit)]
	}
	if len(jobs)+len(fanouts) == 0 {
		return pluginjobs.ArchiveResult{}, tx.Commit(ctx)
	}
	var taskCount, fanoutCount, uncancelled int
	var taskBytes, fanoutBytes int64
	if err = tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(data)),0) FROM np_plugin_job_archives`).Scan(&taskCount, &taskBytes); err != nil {
		return pluginjobs.ArchiveResult{}, err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(data)),0),count(*) FILTER (WHERE NOT cancelled) FROM np_plugin_fanout_archives`).Scan(&fanoutCount, &fanoutBytes, &uncancelled); err != nil {
		return pluginjobs.ArchiveResult{}, err
	}
	if taskCount+len(jobs) > pluginjobs.MaxArchivedJobs || fanoutCount+len(fanouts) > pluginjobs.MaxArchivedFanouts {
		return pluginjobs.ArchiveResult{}, pluginjobs.ErrCapacity
	}
	required := taskBytes + fanoutBytes + int64(uncancelled)*int64(len(`,"cancelled":true`))
	for _, j := range jobs {
		archived, e := pluginjobs.ArchiveJob(j, now)
		if e != nil {
			return pluginjobs.ArchiveResult{}, e
		}
		raw, e := json.Marshal(archived)
		if e != nil {
			return pluginjobs.ArchiveResult{}, e
		}
		required += int64(len(raw))
		if required > pluginjobs.MaxArchiveBytes || len(raw) > 4096 {
			return pluginjobs.ArchiveResult{}, pluginjobs.ErrCapacity
		}
		if _, e = tx.Exec(ctx, `INSERT INTO np_plugin_job_archives(id,plugin,status,updated_at,archived_at,data,reissue_id) VALUES($1,$2,$3,$4,$5,$6,$7)`,
			archived.ID, archived.Plugin, archived.Status, archived.UpdatedAt, archived.ArchivedAt, raw, storedReissueID(archived.Review)); e != nil {
			return pluginjobs.ArchiveResult{}, e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM np_plugin_jobs WHERE id=$1`, j.ID); e != nil {
			return pluginjobs.ArchiveResult{}, e
		}
	}
	for _, f := range fanouts {
		archived := pluginjobs.ArchivedFanout{Receipt: f, ArchivedAt: now.UTC()}
		if err = pluginjobs.ValidateArchivedFanout(archived); err != nil {
			return pluginjobs.ArchiveResult{}, err
		}
		raw, e := json.Marshal(archived)
		if e != nil {
			return pluginjobs.ArchiveResult{}, e
		}
		required += int64(len(raw))
		if !f.Cancelled {
			required += int64(len(`,"cancelled":true`))
		}
		if required > pluginjobs.MaxArchiveBytes || len(raw) > 524288 {
			return pluginjobs.ArchiveResult{}, pluginjobs.ErrCapacity
		}
		if _, e = tx.Exec(ctx, `INSERT INTO np_plugin_fanout_archives(event_key,archived_at,cancelled,data) VALUES($1,$2,$3,$4)`,
			f.Event.Key, archived.ArchivedAt, f.Cancelled, raw); e != nil {
			return pluginjobs.ArchiveResult{}, e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM np_plugin_fanouts WHERE event_key=$1`, f.Event.Key); e != nil {
			return pluginjobs.ArchiveResult{}, e
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return pluginjobs.ArchiveResult{}, err
	}
	return pluginjobs.ArchiveResult{Tasks: len(jobs), Fanouts: len(fanouts)}, nil
}

// A missing archive leaves admission to the active queue. Keep this lookup
// separate from active claim authorization; history can never authorize work.
func (r *PluginJobRepository) archivedTask(ctx context.Context, tx pgx.Tx, id string) (pluginjobs.ArchivedJob, bool, error) {
	j, err := scanArchivedTask(tx.QueryRow(ctx, `SELECT `+archivedTaskColumns+` FROM np_plugin_job_archives WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return pluginjobs.ArchivedJob{}, false, nil
	}
	return j, err == nil, err
}
