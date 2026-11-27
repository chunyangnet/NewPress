package database

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/pluginjobs"
)

var _ pluginjobs.ScopedClaimer = (*PluginJobRepository)(nil)

// ClaimScoped acquires valid work only for a locally verified package/epoch.
// Revoked work can be acquired on any instance for the existing cancellation
// path. Callbacks, filesystem reads and guest execution stay outside this tx.
func (r *PluginJobRepository) ClaimScoped(ctx context.Context, now time.Time, scopes []pluginjobs.ClaimScope) (pluginjobs.Job, bool, error) {
	if now.IsZero() || len(scopes) > plugin.MaxTaskControls {
		return pluginjobs.Job{}, false, pluginjobs.ErrInvalid
	}
	local := make(map[string]string, len(scopes))
	for _, scope := range scopes {
		token, err := hex.DecodeString(scope.Epoch)
		if packagefs.ValidateSlug(scope.Plugin) != nil || err != nil || len(token) != 32 ||
			hex.EncodeToString(token) != scope.Epoch || local[scope.Plugin] != "" {
			return pluginjobs.Job{}, false, pluginjobs.ErrInvalid
		}
		local[scope.Plugin] = scope.Epoch
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return pluginjobs.Job{}, false, plugin.ErrTaskControlUnavailable
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_jobs IN ROW SHARE MODE`); err != nil {
		return pluginjobs.Job{}, false, plugin.ErrTaskControlUnavailable
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
	var databaseNow time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return pluginjobs.Job{}, false, plugin.ErrTaskControlUnavailable
	}
	if databaseNow.After(now) {
		now = databaseNow
	}
	for {
		// Read the candidate identity, then lock authorization before the task,
		// matching CheckSharedClaim and restore's lock order. A skipped valid
		// task never acquires a lease or consumes an attempt.
		var id, slug string
		err = tx.QueryRow(ctx, `SELECT id,plugin FROM np_plugin_jobs WHERE updated_at <= $1 AND NOT(id=ANY($2::text[])) AND
 ((status IN ('pending','retry') AND available_at <= $1) OR (status='running' AND lease_until <= $1))
 ORDER BY available_at,id LIMIT 1`, now, excluded).Scan(&id, &slug)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.Commit(ctx)
			return pluginjobs.Job{}, false, err
		}
		if err != nil {
			return pluginjobs.Job{}, false, err
		}
		excluded = append(excluded, id)
		control, controlErr := scanTaskControl(tx.QueryRow(ctx, `SELECT `+pluginTaskControlColumns+` FROM np_plugin_task_authorizations WHERE slug=$1 FOR SHARE`, slug))
		found := controlErr == nil
		if controlErr != nil && !errors.Is(controlErr, pgx.ErrNoRows) {
			return pluginjobs.Job{}, false, taskControlReadError(controlErr)
		}
		old, err := scanTask(tx.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$2 AND plugin=$3 AND updated_at <= $1 AND
 ((status IN ('pending','retry') AND available_at <= $1) OR (status='running' AND lease_until <= $1)) FOR UPDATE SKIP LOCKED`, now, id, slug))
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return pluginjobs.Job{}, false, err
		}
		if found && plugin.TaskControlAllows(control, old, now) && local[old.Plugin] != old.Epoch {
			continue
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
		if next.Version == old.Version {
			continue
		}
		if err = saveTask(ctx, tx, next); err != nil {
			return pluginjobs.Job{}, false, err
		}
		if !ok {
			continue
		}
		if err = tx.Commit(ctx); err != nil {
			return pluginjobs.Job{}, false, err
		}
		return claimed, true, nil
	}
}
