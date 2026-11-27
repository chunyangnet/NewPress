package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/pluginjobs"
)

// PluginJobRepository stores one row per task. Shared domain transitions run on
// the locked row only; no SQL implementation of the state machine can drift
// from the file implementation. Admission is serialized separately from claims.
type PluginJobRepository struct{ pool *pgxpool.Pool }

func NewPluginJobRepository(pool *pgxpool.Pool) *PluginJobRepository {
	return &PluginJobRepository{pool: pool}
}

var _ pluginjobs.Store = (*PluginJobRepository)(nil)

const pluginTaskColumns = "id,plugin,status,available_at,updated_at,lease_until,data,reissue_id"

var errPluginTaskRow = errors.New("inconsistent stored plugin task")

func taskQueue(job *pluginjobs.Job) (*pluginjobs.Queue, error) {
	b, err := pluginjobs.NewFileBackend("")
	if err != nil {
		return nil, err
	}
	if job != nil {
		err = b.Update(context.Background(), func(s *pluginjobs.State) error { s.Jobs[job.ID] = *job; return nil })
	}
	return &pluginjobs.Queue{Backend: b}, err
}
func decodeTask(raw []byte) (pluginjobs.Job, error) {
	j, err := packagefs.DecodeJSON[pluginjobs.Job](bytes.NewReader(raw), 65536, "plugin task")
	if err != nil {
		return pluginjobs.Job{}, err
	}
	s := pluginjobs.InitialState()
	s.Jobs[j.ID] = j
	if err = pluginjobs.ValidateState(s); err != nil {
		return pluginjobs.Job{}, err
	}
	return j, nil
}

// SQL columns select and scope tasks; JSON owns their state transitions. Check
// both representations before returning or changing a selected row. Otherwise
// a damaged index value can expose another plugin's task or redirect a restore
// write to the payload's ID. pgx stores SQL timestamps at microsecond precision.
func scanTask(row interface{ Scan(...any) error }) (pluginjobs.Job, error) {
	var id, plugin, status string
	var availableAt, updatedAt time.Time
	var leaseUntil *time.Time
	var raw []byte
	var reissueID *string
	if err := row.Scan(&id, &plugin, &status, &availableAt, &updatedAt, &leaseUntil, &raw, &reissueID); err != nil {
		return pluginjobs.Job{}, err
	}
	j, err := decodeTask(raw)
	if err != nil {
		return pluginjobs.Job{}, err
	}
	if err = validateTaskIndex(j, id, plugin, status, availableAt, updatedAt, leaseUntil, reissueID); err != nil {
		return pluginjobs.Job{}, err
	}
	return j, nil
}

func validateTaskIndex(j pluginjobs.Job, id, plugin, status string, availableAt, updatedAt time.Time, leaseUntil *time.Time, reissueID *string) error {
	if id != j.ID || plugin != j.Plugin || status != j.Status ||
		!availableAt.Equal(j.AvailableAt.Truncate(time.Microsecond)) || !updatedAt.Equal(j.UpdatedAt.Truncate(time.Microsecond)) ||
		(leaseUntil == nil) != j.LeaseUntil.IsZero() ||
		(leaseUntil != nil && !leaseUntil.Equal(j.LeaseUntil.Truncate(time.Microsecond))) || !sameStoredReissueID(reissueID, j.Review) {
		return errPluginTaskRow
	}
	return nil
}

func saveTask(ctx context.Context, tx pgx.Tx, j pluginjobs.Job) error {
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	var lease *time.Time
	if !j.LeaseUntil.IsZero() {
		lease = &j.LeaseUntil
	}
	_, err = tx.Exec(ctx, `INSERT INTO np_plugin_jobs(id,plugin,status,available_at,updated_at,lease_until,data,reissue_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO UPDATE SET
 status=EXCLUDED.status,available_at=EXCLUDED.available_at,updated_at=EXCLUDED.updated_at,
	lease_until=EXCLUDED.lease_until,data=EXCLUDED.data,reissue_id=EXCLUDED.reissue_id`, j.ID, j.Plugin, j.Status, j.AvailableAt, j.UpdatedAt, lease, raw, storedReissueID(j.Review))
	return err
}
func (r *PluginJobRepository) Enqueue(ctx context.Context, in pluginjobs.Input, now time.Time) (pluginjobs.Job, error) {
	q, err := taskQueue(nil)
	if err != nil {
		return pluginjobs.Job{}, err
	}
	candidate, err := q.Enqueue(ctx, in, now)
	if err != nil {
		return pluginjobs.Job{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return pluginjobs.Job{}, err
	}
	defer tx.Rollback(context.Background())
	// This lock protects capacity and duplicate admission, not task execution.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-pluginjobs-admission',0))`); err != nil {
		return pluginjobs.Job{}, err
	}
	old, err := scanTask(tx.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1 FOR UPDATE`, candidate.ID))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return pluginjobs.Job{}, err
	}
	archived, found, archiveErr := r.archivedTask(ctx, tx, candidate.ID)
	if archiveErr != nil {
		return pluginjobs.Job{}, archiveErr
	}
	if found && err == nil {
		return pluginjobs.Job{}, errPluginTaskRow
	}
	if err == nil {
		if old.Fingerprint != candidate.Fingerprint {
			return pluginjobs.Job{}, pluginjobs.ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return pluginjobs.Job{}, err
		}
		return old, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return pluginjobs.Job{}, err
	}
	if found {
		replay, err := archived.Replay(in)
		if err != nil {
			return pluginjobs.Job{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return pluginjobs.Job{}, err
		}
		return replay, nil
	}
	if err = pluginQuarantineAdmission(ctx, tx, true); err != nil {
		return pluginjobs.Job{}, err
	}
	if err = reserveTaskCapacity(ctx, tx, candidate); err != nil {
		return pluginjobs.Job{}, err
	}
	if err = saveTask(ctx, tx, candidate); err != nil {
		return pluginjobs.Job{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return pluginjobs.Job{}, err
	}
	return candidate, nil
}
func (r *PluginJobRepository) CheckClaim(ctx context.Context, claim pluginjobs.Job, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	// Take the task table before reading quarantine, matching maintenance's
	// order. Its exclusive move cannot commit midway through this check.
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_jobs IN ROW SHARE MODE`); err != nil {
		return err
	}
	if err = sharedQuarantineGate(ctx, tx); err != nil {
		return err
	}
	records, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return err
	}
	blocked, err := pluginjobs.QuarantineWorkPolicy(records)
	if errors.Is(err, pluginjobs.ErrQuarantined) || blocked[claim.ID] {
		return pluginjobs.ErrAuthorization
	}
	if err != nil {
		return err
	}
	current, err := scanTask(tx.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1`, claim.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return pluginjobs.ErrAuthorization
	}
	if err != nil {
		return err
	}
	if err = pluginjobs.ValidateClaim(current, claim, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PluginJobRepository) Claim(ctx context.Context, now time.Time) (pluginjobs.Job, bool, error) {
	if now.IsZero() {
		return pluginjobs.Job{}, false, pluginjobs.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return pluginjobs.Job{}, false, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_jobs IN ROW SHARE MODE`); err != nil {
		return pluginjobs.Job{}, false, err
	}
	if err = sharedQuarantineGate(ctx, tx); err != nil {
		return pluginjobs.Job{}, false, err
	}
	quarantines, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return pluginjobs.Job{}, false, err
	}
	blocked, err := pluginjobs.QuarantineWorkPolicy(quarantines)
	if err != nil {
		return pluginjobs.Job{}, false, err
	}
	excluded := make([]string, 0, len(blocked))
	for id := range blocked {
		excluded = append(excluded, id)
	}
	for {
		old, err := scanTask(tx.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE updated_at <= $1 AND NOT(id=ANY($2::text[])) AND
   ((status IN ('pending','retry') AND available_at <= $1) OR (status='running' AND lease_until <= $1))
   ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now, excluded))
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.Commit(ctx)
			return pluginjobs.Job{}, false, err
		}
		if err != nil {
			return pluginjobs.Job{}, false, err
		}
		q, err := taskQueue(&old)
		if err != nil {
			return pluginjobs.Job{}, false, err
		}
		claimed, ok, err := q.Claim(ctx, now)
		if err != nil {
			return pluginjobs.Job{}, false, err
		}
		state, err := q.Backend.Read(ctx)
		if err != nil {
			return pluginjobs.Job{}, false, err
		}
		next := state.Jobs[old.ID]
		// SQL timestamps have microsecond precision; JSON retains nanoseconds. If
		// SQL selected a row just before its exact deadline, do not acknowledge it.
		if next.Version == old.Version {
			err = tx.Commit(ctx)
			return pluginjobs.Job{}, false, err
		}
		if err = saveTask(ctx, tx, next); err != nil {
			return pluginjobs.Job{}, false, err
		}
		if !ok {
			continue
		} // persist exhausted leases and seek the next runnable task
		if err = tx.Commit(ctx); err != nil {
			return pluginjobs.Job{}, false, err
		}
		return claimed, true, nil
	}
}
func (r *PluginJobRepository) mutate(ctx context.Context, id string, sharedGate bool, fn func(*pluginjobs.Queue, map[string]pluginjobs.QuarantinedRecord) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	old, err := scanTask(tx.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return pluginjobs.ErrConflict
	} else if err != nil {
		return err
	}
	if sharedGate {
		if err = sharedQuarantineGate(ctx, tx); err != nil {
			return err
		}
	}
	quarantines, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return err
	}
	q, err := taskQueue(&old)
	if err != nil {
		return err
	}
	if err = fn(q, quarantines); err != nil {
		return err
	}
	s, err := q.Backend.Read(ctx)
	if err != nil {
		return err
	}
	if err = saveTask(ctx, tx, s.Jobs[id]); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *PluginJobRepository) List(ctx context.Context, query pluginjobs.Query) (pluginjobs.Page, error) {
	query, err := pluginjobs.NormalizeQuery(query)
	if err != nil {
		return pluginjobs.Page{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return pluginjobs.Page{}, err
	}
	defer tx.Rollback(context.Background())
	page := pluginjobs.Page{Items: []pluginjobs.Summary{}}
	table, columns := "np_plugin_jobs", pluginTaskColumns
	if query.Archived {
		table, columns = "np_plugin_job_archives", archivedTaskColumns
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE plugin=$1 AND ($2='' OR status=$2)`, query.Plugin, query.Status).Scan(&page.Total); err != nil {
		return pluginjobs.Page{}, err
	}
	rows, err := tx.Query(ctx, `SELECT `+columns+` FROM `+table+` WHERE plugin=$1 AND ($2='' OR status=$2) ORDER BY updated_at DESC,id LIMIT $3 OFFSET $4`, query.Plugin, query.Status, query.Limit, query.Offset)
	if err != nil {
		return pluginjobs.Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		if query.Archived {
			job, e := scanArchivedTask(rows)
			if e != nil {
				return pluginjobs.Page{}, e
			}
			page.Items = append(page.Items, job.Summary)
			continue
		}
		job, e := scanTask(rows)
		if e != nil {
			return pluginjobs.Page{}, e
		}
		page.Items = append(page.Items, pluginjobs.Summarize(job))
	}
	if err = rows.Err(); err != nil {
		return pluginjobs.Page{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return pluginjobs.Page{}, err
	}
	return page, nil
}

func (r *PluginJobRepository) Finish(ctx context.Context, j pluginjobs.Job, code string, now time.Time) error {
	return r.mutate(ctx, j.ID, true, func(q *pluginjobs.Queue, records map[string]pluginjobs.QuarantinedRecord) error {
		blocked, err := pluginjobs.QuarantineWorkPolicy(records)
		if err != nil || blocked[j.ID] {
			return pluginjobs.ErrConflict
		}
		return q.Finish(ctx, j, code, now)
	})
}
func (r *PluginJobRepository) Change(ctx context.Context, plugin, id string, version int64, action string, now time.Time) error {
	return r.mutate(ctx, id, action == "retry", func(q *pluginjobs.Queue, records map[string]pluginjobs.QuarantinedRecord) error {
		if action == "retry" {
			blocked, err := pluginjobs.QuarantineWorkPolicy(records)
			if err != nil || blocked[id] {
				return pluginjobs.ErrQuarantined
			}
		}
		return q.Change(ctx, plugin, id, version, action, now)
	})
}
