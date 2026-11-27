package database

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/pluginjobs"
)

var _ pluginjobs.ReviewStore = (*PluginJobRepository)(nil)

func storedReissueID(review *pluginjobs.ReissueProvenance) *string {
	if review == nil {
		return nil
	}
	return &review.RequestID
}

func sameStoredReissueID(id *string, review *pluginjobs.ReissueProvenance) bool {
	if review == nil {
		return id == nil
	}
	return id != nil && *id == review.RequestID
}

// Admission holds the task advisory lock while using this common byte/count
// reservation. Claims and completion consume the space reserved here.
func reserveTaskCapacity(ctx context.Context, tx pgx.Tx, task pluginjobs.Job) error {
	var count, pluginCount, mutableCount int
	var size int64
	if err := tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(data)),0),count(*) FILTER (WHERE plugin=$1),count(*) FILTER (WHERE status NOT IN ('succeeded','cancelled')) FROM np_plugin_jobs`, task.Plugin).Scan(&count, &size, &pluginCount, &mutableCount); err != nil {
		return err
	}
	raw, err := json.Marshal(task)
	if err != nil {
		return err
	}
	required := size + int64(len(raw)) + int64(mutableCount+1)*pluginjobs.TaskTransitionReserveBytes
	if count >= pluginjobs.MaxJobs || pluginCount >= pluginjobs.MaxPluginJobs || required > pluginjobs.MaxStateBytes {
		return pluginjobs.ErrCapacity
	}
	return nil
}

type storedTaskReceipt struct {
	Summary     pluginjobs.Summary
	Fingerprint string
	Review      *pluginjobs.ReissueProvenance
}

// One statement observes either side of an archive move. The lookup column is
// selected only by internal callers; user input is always a bound parameter.
func findStoredTask(ctx context.Context, db fanoutQuerier, column, key string) (storedTaskReceipt, bool, error) {
	if column != "id" && column != "reissue_id" {
		return storedTaskReceipt{}, false, pluginjobs.ErrInvalid
	}
	predicate := column + "=$1"
	if column == "reissue_id" {
		// An inconsistent index must not hide a prior command when the new epoch
		// produces another task ID. The bounded task/history byte scan selects
		// potential receipts for normal decoding and SQL/JSON consistency checks.
		// A digest embedded in an unrelated payload is filtered after decoding.
		predicate += " OR position(convert_to($1,'UTF8') IN data)>0"
	}
	rows, err := db.Query(ctx, `SELECT id,plugin,status,available_at,updated_at,lease_until,NULL::timestamptz,data,reissue_id FROM np_plugin_jobs WHERE `+predicate+`
 UNION ALL SELECT id,plugin,status,NULL::timestamptz,updated_at,NULL::timestamptz,archived_at,data,reissue_id FROM np_plugin_job_archives WHERE `+predicate, key)
	if err != nil {
		return storedTaskReceipt{}, false, err
	}
	defer rows.Close()
	var out storedTaskReceipt
	found := false
	for rows.Next() {
		var candidate storedTaskReceipt
		var id, plugin, status string
		var updatedAt time.Time
		var availableAt, leaseUntil, archivedAt *time.Time
		var raw []byte
		var reissueID *string
		if err = rows.Scan(&id, &plugin, &status, &availableAt, &updatedAt, &leaseUntil, &archivedAt, &raw, &reissueID); err != nil {
			return storedTaskReceipt{}, false, err
		}
		if archivedAt == nil {
			task, e := decodeTask(raw)
			if e != nil {
				return storedTaskReceipt{}, false, e
			}
			if availableAt == nil {
				return storedTaskReceipt{}, false, errPluginTaskRow
			}
			if e = validateTaskIndex(task, id, plugin, status, *availableAt, updatedAt, leaseUntil, reissueID); e != nil {
				return storedTaskReceipt{}, false, e
			}
			candidate = storedTaskReceipt{Summary: pluginjobs.Summarize(task), Fingerprint: task.Fingerprint, Review: task.Review}
		} else {
			task, e := decodeArchivedTask(raw)
			if e != nil {
				return storedTaskReceipt{}, false, e
			}
			if availableAt != nil || leaseUntil != nil {
				return storedTaskReceipt{}, false, errPluginTaskRow
			}
			if e = validateArchivedTaskIndex(task, id, plugin, status, updatedAt, *archivedAt, reissueID); e != nil {
				return storedTaskReceipt{}, false, e
			}
			candidate = storedTaskReceipt{Summary: task.Summary, Fingerprint: task.Fingerprint, Review: task.Review}
		}
		if column == "reissue_id" && (candidate.Review == nil || candidate.Review.RequestID != key) {
			continue
		}
		if found {
			return storedTaskReceipt{}, false, errPluginTaskRow
		}
		out = candidate
		found = true
	}
	if err = rows.Err(); err != nil {
		return storedTaskReceipt{}, false, err
	}
	return out, found, nil
}

func (r *PluginJobRepository) GetReview(ctx context.Context, plugin, id string) (pluginjobs.ReviewDetails, error) {
	if _, err := pluginjobs.NormalizeQuery(pluginjobs.Query{Plugin: plugin}); err != nil || len(id) != 64 {
		return pluginjobs.ReviewDetails{}, pluginjobs.ErrInvalid
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return pluginjobs.ReviewDetails{}, pluginjobs.ErrInvalid
		}
	}
	task, found, err := findStoredTask(ctx, r.pool, "id", id)
	if err != nil {
		return pluginjobs.ReviewDetails{}, err
	}
	if !found || task.Summary.Plugin != plugin {
		return pluginjobs.ReviewDetails{}, pluginjobs.ErrConflict
	}
	return pluginjobs.NewReviewDetails(task.Summary, task.Review), nil
}

func (r *PluginJobRepository) Reissue(ctx context.Context, request pluginjobs.ReissueRequest, now time.Time) (pluginjobs.Summary, error) {
	request, receipt, err := pluginjobs.PrepareReissue(request)
	if err != nil {
		return pluginjobs.Summary{}, err
	}
	if now.IsZero() {
		return pluginjobs.Summary{}, pluginjobs.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return pluginjobs.Summary{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-pluginjobs-admission',0))`); err != nil {
		return pluginjobs.Summary{}, err
	}
	old, found, err := findStoredTask(ctx, tx, "reissue_id", receipt.ID)
	if err != nil {
		return pluginjobs.Summary{}, err
	}
	if found {
		if old.Summary.Plugin != request.Input.Plugin {
			return pluginjobs.Summary{}, errPluginTaskRow
		}
		if err = pluginjobs.MatchReissue(old.Review, request, receipt); err != nil {
			return pluginjobs.Summary{}, err
		}
		return old.Summary, tx.Commit(ctx)
	}
	if !request.AllowNew {
		return pluginjobs.Summary{}, pluginjobs.ErrAuthorization
	}
	if err = pluginQuarantineAdmission(ctx, tx, true); err != nil {
		return pluginjobs.Summary{}, err
	}
	// A failed source can still be retried by another administrator. Lock its
	// row so the checked source version cannot change before child admission.
	active, activeErr := scanTask(tx.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1 FOR UPDATE`, request.SourceID))
	if activeErr != nil && !errors.Is(activeErr, pgx.ErrNoRows) {
		return pluginjobs.Summary{}, activeErr
	}
	history, archived, err := r.archivedTask(ctx, tx, request.SourceID)
	if err != nil {
		return pluginjobs.Summary{}, err
	}
	if activeErr == nil && archived {
		return pluginjobs.Summary{}, errPluginTaskRow
	}
	var source storedTaskReceipt
	if activeErr == nil {
		source = storedTaskReceipt{Summary: pluginjobs.Summarize(active), Fingerprint: active.Fingerprint}
	} else if archived {
		source = storedTaskReceipt{Summary: history.Summary, Fingerprint: history.Fingerprint}
	} else {
		return pluginjobs.Summary{}, pluginjobs.ErrConflict
	}
	child, err := pluginjobs.BuildReissuedJob(request, source.Summary, source.Fingerprint, now)
	if err != nil {
		return pluginjobs.Summary{}, err
	}
	if _, found, e := findStoredTask(ctx, tx, "id", child.ID); e != nil {
		return pluginjobs.Summary{}, e
	} else if found {
		return pluginjobs.Summary{}, pluginjobs.ErrConflict
	}
	if _, err = taskQueue(&child); err != nil {
		return pluginjobs.Summary{}, err
	}
	if err = reserveTaskCapacity(ctx, tx, child); err != nil {
		return pluginjobs.Summary{}, err
	}
	if err = saveTask(ctx, tx, child); err != nil {
		return pluginjobs.Summary{}, err
	}
	return pluginjobs.Summarize(child), tx.Commit(ctx)
}
