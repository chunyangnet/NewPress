package database

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"

	"newpress.local/newpress/internal/pluginjobs"
)

var _ pluginjobs.FanoutStore = (*PluginJobRepository)(nil)
var _ pluginjobs.FanoutReader = (*PluginJobRepository)(nil)

func (r *PluginJobRepository) FindFanout(ctx context.Context, event pluginjobs.Event) (pluginjobs.FanoutReceipt, bool, error) {
	if err := pluginjobs.ValidateFanout(pluginjobs.FanoutReceipt{Event: event}); err != nil {
		return pluginjobs.FanoutReceipt{}, false, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return pluginjobs.FanoutReceipt{}, false, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_fanout_archives, np_plugin_fanouts IN ROW SHARE MODE`); err != nil {
		return pluginjobs.FanoutReceipt{}, false, err
	}
	receipt, found, err := findStoredFanout(ctx, tx, event)
	if err != nil {
		return pluginjobs.FanoutReceipt{}, false, err
	}
	if err = checkPGFanoutQuarantines(ctx, tx, receipt, found); err != nil {
		return pluginjobs.FanoutReceipt{}, false, err
	}
	return receipt, found, tx.Commit(ctx)
}

func (r *PluginJobRepository) FreezeFanout(ctx context.Context, event pluginjobs.Event, subscriptions []pluginjobs.Subscription) (pluginjobs.FanoutReceipt, error) {
	candidate := pluginjobs.FanoutReceipt{Event: event, Subscriptions: append([]pluginjobs.Subscription{}, subscriptions...)}
	if err := pluginjobs.ValidateFanout(candidate); err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-plugin-fanout-admission',0))`); err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	old, found, err := findStoredFanout(ctx, tx, event)
	if err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	quarantines, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	if found {
		if !old.Cancelled && !old.Delivered {
			if err = pluginjobs.CheckQuarantineFanout(quarantines, event.Key); err != nil {
				return pluginjobs.FanoutReceipt{}, err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return pluginjobs.FanoutReceipt{}, err
		}
		return old, nil
	}
	if err = sharedQuarantineGate(ctx, tx); err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	if err = pluginjobs.QuarantineAdmission(quarantines, false); err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	var count int
	var size int64
	if err = tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(data)),0) FROM np_plugin_fanouts`).Scan(&count, &size); err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	// Reserve the cancellation marker for every existing receipt as well as
	// this one, so offline restore cannot exceed storage limits at capacity.
	const cancellationReserve = int64(len(`,"cancelled":true,"delivered":true`))
	if count >= pluginjobs.MaxFanouts || int64(len(raw))+cancellationReserve > 524288 || size+int64(len(raw))+int64(count+1)*cancellationReserve > pluginjobs.MaxStateBytes {
		return pluginjobs.FanoutReceipt{}, pluginjobs.ErrCapacity
	}
	if _, err = tx.Exec(ctx, `INSERT INTO np_plugin_fanouts(event_key,data) VALUES($1,$2)`, event.Key, raw); err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return pluginjobs.FanoutReceipt{}, err
	}
	return candidate, nil
}

func checkPGFanoutQuarantines(ctx context.Context, tx pgx.Tx, receipt pluginjobs.FanoutReceipt, found bool) error {
	records, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return err
	}
	if found && !receipt.Cancelled && !receipt.Delivered {
		return pluginjobs.CheckQuarantineFanout(records, receipt.Event.Key)
	}
	return nil
}
