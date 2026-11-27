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
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/pluginjobs"
)

type PluginTaskControlRepository struct{ pool *pgxpool.Pool }

func NewPluginTaskControlRepository(pool *pgxpool.Pool) *PluginTaskControlRepository {
	return &PluginTaskControlRepository{pool: pool}
}

var _ plugin.TaskControlStore = (*PluginTaskControlRepository)(nil)

const pluginTaskControlColumns = "slug,revision,epoch,enabled,retired,data"

func loadAllTaskControls(ctx context.Context, tx pgx.Tx, lock bool) ([]plugin.TaskControl, error) {
	controls, _, err := loadBudgetedTaskControls(ctx, tx, lock)
	return controls, err
}

// Return reservations from the same validated snapshot. A caller holding the
// admission lock may reuse these values without re-encoding every authority.
func loadBudgetedTaskControls(ctx context.Context, tx pgx.Tx, lock bool) ([]plugin.TaskControl, map[string]int, error) {
	var count, total int64
	if err := tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(octet_length(data)),0) FROM np_plugin_task_authorizations`).Scan(&count, &total); err != nil {
		return nil, nil, plugin.ErrTaskControlUnavailable
	}
	if count > plugin.MaxTaskControls || total > plugin.MaxTaskControlTotal {
		return nil, nil, plugin.ErrTaskControlCorrupt
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	rows, err := tx.Query(ctx, `SELECT `+pluginTaskControlColumns+` FROM np_plugin_task_authorizations ORDER BY slug`+suffix)
	if err != nil {
		return nil, nil, plugin.ErrTaskControlUnavailable
	}
	defer rows.Close()
	controls := make([]plugin.TaskControl, 0, count)
	reservations := make(map[string]int, count)
	budget := 0
	for rows.Next() {
		c, reserved, err := scanBudgetedTaskControl(rows)
		if err != nil {
			return nil, nil, taskControlReadError(err)
		}
		if reserved > plugin.MaxTaskControlTotal-budget {
			return nil, nil, plugin.ErrTaskControlCorrupt
		}
		budget += reserved
		reservations[c.Slug] = reserved
		controls = append(controls, c)
	}
	if rows.Err() != nil {
		return nil, nil, plugin.ErrTaskControlUnavailable
	}
	return controls, reservations, nil
}

// Readiness includes schema, storage limits and every index/JSON pairing. A
// malformed row must not disappear from the authority view by changing slug.
func (r *PluginTaskControlRepository) Ready(ctx context.Context) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return plugin.ErrTaskControlUnavailable
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_task_authorizations IN ACCESS SHARE MODE`); err != nil {
		return plugin.ErrTaskControlUnavailable
	}
	if _, err = loadSharedQuarantines(ctx, tx); err != nil {
		return err
	}
	if _, err = loadAllTaskControls(ctx, tx, false); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListTaskControls discovers shared subscriptions in a complete checked
// snapshot, even when this instance has no matching local installation.
func (r *PluginTaskControlRepository) ListTaskControls(ctx context.Context) ([]plugin.TaskControl, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, plugin.ErrTaskControlUnavailable
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_task_authorizations IN ACCESS SHARE MODE`); err != nil {
		return nil, plugin.ErrTaskControlUnavailable
	}
	if err = sharedQuarantineGate(ctx, tx); err != nil {
		return nil, err
	}
	controls, err := loadAllTaskControls(ctx, tx, false)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, plugin.ErrTaskControlUnavailable
	}
	return controls, nil
}

func scanTaskControl(row interface{ Scan(...any) error }) (plugin.TaskControl, error) {
	c, _, err := scanBudgetedTaskControl(row)
	return c, err
}

func scanBudgetedTaskControl(row interface{ Scan(...any) error }) (plugin.TaskControl, int, error) {
	var slug, epoch string
	var revision int64
	var enabled, retired bool
	var raw []byte
	if err := row.Scan(&slug, &revision, &epoch, &enabled, &retired, &raw); err != nil {
		return plugin.TaskControl{}, 0, err
	}
	c, err := packagefs.DecodeJSON[plugin.TaskControl](bytes.NewReader(raw), plugin.MaxTaskControlBytes, "shared task authorization")
	if err != nil || c.Slug != slug || c.Revision != revision ||
		c.Epoch.Token != epoch || c.Enabled != enabled || c.Retired != retired {
		return plugin.TaskControl{}, 0, plugin.ErrTaskControlCorrupt
	}
	// BudgetBytes performs the complete domain validation before reserving
	// lifecycle metadata. Both checks are mandatory, but need run only once.
	reserved, err := plugin.TaskControlBudgetBytes(c)
	if err != nil {
		return plugin.TaskControl{}, 0, plugin.ErrTaskControlCorrupt
	}
	return c, reserved, nil
}

func taskControlReadError(err error) error {
	if errors.Is(err, plugin.ErrTaskControlCorrupt) {
		return plugin.ErrTaskControlCorrupt
	}
	return plugin.ErrTaskControlUnavailable
}

func (r *PluginTaskControlRepository) LoadTaskControl(ctx context.Context, slug string) (plugin.TaskControl, bool, error) {
	if packagefs.ValidateSlug(slug) != nil {
		return plugin.TaskControl{}, false, plugin.ErrInactive
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return plugin.TaskControl{}, false, plugin.ErrTaskControlUnavailable
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_task_authorizations IN ACCESS SHARE MODE`); err != nil {
		return plugin.TaskControl{}, false, plugin.ErrTaskControlUnavailable
	}
	if err = sharedQuarantineGate(ctx, tx); err != nil {
		return plugin.TaskControl{}, false, err
	}
	c, err := scanTaskControl(tx.QueryRow(ctx, `SELECT `+pluginTaskControlColumns+` FROM np_plugin_task_authorizations WHERE slug=$1`, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return plugin.TaskControl{}, false, nil
	}
	if err != nil {
		return plugin.TaskControl{}, false, taskControlReadError(err)
	}
	return c, true, tx.Commit(ctx)
}

func saveTaskControl(ctx context.Context, tx pgx.Tx, c plugin.TaskControl) error {
	if err := plugin.ValidateTaskControl(c); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return plugin.ErrTaskControlCorrupt
	}
	_, err = tx.Exec(ctx, `INSERT INTO np_plugin_task_authorizations(slug,revision,epoch,enabled,retired,data)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(slug) DO UPDATE SET
 revision=EXCLUDED.revision,epoch=EXCLUDED.epoch,enabled=EXCLUDED.enabled,retired=EXCLUDED.retired,data=EXCLUDED.data`,
		c.Slug, c.Revision, c.Epoch.Token, c.Enabled, c.Retired, raw)
	return err
}

func (r *PluginTaskControlRepository) SaveTaskControl(ctx context.Context, expected int64, next plugin.TaskControl) error {
	if expected < 0 || next.Revision < 1 || next.Revision-1 != expected {
		return plugin.ErrTaskControlConflict
	}
	if err := plugin.ValidateTaskControl(next); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return plugin.ErrTaskControlUnavailable
	}
	defer tx.Rollback(context.Background())
	// Capacity, first insertion and CAS share one short transaction lock. Reads
	// and guest execution do not take this lock or hold a DB transaction open.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-plugin-task-control-admission',0))`); err != nil {
		return plugin.ErrTaskControlUnavailable
	}
	if err = sharedQuarantineGate(ctx, tx); err != nil {
		return err
	}
	old, err := scanTaskControl(tx.QueryRow(ctx, `SELECT `+pluginTaskControlColumns+` FROM np_plugin_task_authorizations WHERE slug=$1 FOR UPDATE`, next.Slug))
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return taskControlReadError(err)
	}
	if (!found && expected != 0) || (found && old.Revision != expected) {
		return plugin.ErrTaskControlConflict
	}
	controls, reservations, err := loadBudgetedTaskControls(ctx, tx, false)
	if err != nil {
		return err
	}
	count, total := 0, 0
	for _, control := range controls {
		if control.Slug == next.Slug {
			continue
		}
		count++
		total += reservations[control.Slug]
	}
	reserved, err := plugin.TaskControlBudgetBytes(next)
	if err != nil || count+1 > plugin.MaxTaskControls || reserved > plugin.MaxTaskControlTotal-total {
		return plugin.ErrTaskControlCapacity
	}
	if err = saveTaskControl(ctx, tx, next); err != nil {
		return taskControlReadError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return plugin.ErrTaskControlUnavailable
	}
	return nil
}

// CheckSharedClaim is a short read check of authorization, claim and quarantine
// facts in one snapshot. It is not a fence for a later business write/network
// send; those still require the separate effect commit protocol.
func (r *PluginJobRepository) CheckSharedClaim(ctx context.Context, claim pluginjobs.Job, now time.Time) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return plugin.ErrTaskControlUnavailable
	}
	defer tx.Rollback(context.Background())
	// Preserve maintenance/restore ordering: task table, quarantine read, then
	// the selected authorization. Authorization writers never acquire tasks.
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_jobs IN ROW SHARE MODE`); err != nil {
		return plugin.ErrTaskControlUnavailable
	}
	if err = sharedQuarantineGate(ctx, tx); err != nil {
		return err
	}
	quarantines, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return err
	}
	blocked, err := pluginjobs.QuarantineWorkPolicy(quarantines)
	if errors.Is(err, pluginjobs.ErrQuarantined) || blocked[claim.ID] {
		return pluginjobs.ErrAuthorization
	}
	if err != nil {
		return err
	}
	control, err := scanTaskControl(tx.QueryRow(ctx, `SELECT `+pluginTaskControlColumns+` FROM np_plugin_task_authorizations WHERE slug=$1 FOR SHARE`, claim.Plugin))
	if errors.Is(err, pgx.ErrNoRows) {
		return pluginjobs.ErrAuthorization
	}
	if err != nil {
		return taskControlReadError(err)
	}
	current, err := scanTask(tx.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1 FOR SHARE`, claim.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return pluginjobs.ErrAuthorization
	}
	if err != nil {
		return err
	}
	var databaseNow time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return plugin.ErrTaskControlUnavailable
	}
	if databaseNow.After(now) {
		now = databaseNow
	}
	if !plugin.TaskControlAllows(control, claim, now) {
		return pluginjobs.ErrAuthorization
	}
	if err = pluginjobs.ValidateClaim(current, claim, now); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return plugin.ErrTaskControlUnavailable
	}
	return nil
}

func suspendRestoredTaskControls(ctx context.Context, tx pgx.Tx) error {
	controls, err := loadAllTaskControls(ctx, tx, true)
	if err != nil {
		return err
	}
	for _, before := range controls {
		if before.Retired && !before.Enabled && before.TransitionID == "" && before.Epoch.ActorID == 0 {
			continue
		}
		next, err := plugin.NextTaskControl(before)
		if err != nil {
			return err
		}
		next.Epoch, err = plugin.NewTaskEpoch(0)
		if err != nil {
			return err
		}
		// Keep values/grants available for an explicit review, but enabling is
		// rejected until a new configuration save retires this restored lifetime.
		next.Enabled, next.Retired, next.TransitionID = false, true, ""
		next.TransitionBytes = 0
		if err = saveTaskControl(ctx, tx, next); err != nil {
			return taskControlReadError(err)
		}
	}
	return nil
}
