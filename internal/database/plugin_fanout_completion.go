package database

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/pluginjobs"
)

var _ pluginjobs.FanoutCompleter = (*PluginJobRepository)(nil)

func (r *PluginJobRepository) CompleteFanout(ctx context.Context, event pluginjobs.Event) error {
	if err := pluginjobs.ValidateFanout(pluginjobs.FanoutReceipt{Event: event}); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT data FROM np_plugin_fanouts WHERE event_key=$1 FOR UPDATE`, event.Key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		receipt, found, err := findStoredFanout(ctx, tx, event)
		if err != nil {
			return err
		}
		if !found {
			return pluginjobs.ErrConflict
		}
		if err = checkPGFanoutQuarantines(ctx, tx, receipt, found); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	receipt, err := decodeFanout(event.Key, raw)
	if err != nil {
		return err
	}
	if !pluginjobs.SameEvent(receipt.Event, event) {
		return pluginjobs.ErrConflict
	}
	if err = checkPGFanoutQuarantines(ctx, tx, receipt, true); err != nil {
		return err
	}
	if !receipt.Cancelled && !receipt.Delivered {
		receipt.Delivered = true
		raw, err = json.Marshal(receipt)
		if err != nil {
			return err
		}
		if len(raw) > 524288 {
			return pluginjobs.ErrCapacity
		}
		if _, err := tx.Exec(ctx, `UPDATE np_plugin_fanouts SET data=$2 WHERE event_key=$1`, event.Key, raw); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
