package main

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/pluginjobs"
)

// This is an offline recovery operation, not a routine startup action. The
// explicit flag acknowledges that other hosts using the database are stopped;
// local directory locks cannot prove that a remote worker is quiescent.
func cancelRestoredPluginTasks(arguments []string) error {
	if len(arguments) != 1 || arguments[0] != "--confirm-all-workers-stopped" {
		return errors.New("usage: newpress plugin cancel-restored-tasks --confirm-all-workers-stopped (stop every application instance first)")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	locks, err := dirlock.Acquire(cfg.DataDir, cfg.ThemesDir, cfg.PluginsDir)
	if err != nil {
		return err
	}
	defer locks.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if cfg.DatabaseURL != "" {
		pool, e := database.OpenWithPoolLimits(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
		if e != nil {
			return e
		}
		defer pool.Close()
		err = database.NewPluginJobRepository(pool).CancelRestored(ctx, time.Now().UTC())
	} else {
		backend, e := pluginjobs.NewFileBackend(filepath.Join(cfg.DataDir, "plugin-jobs.json"))
		if e != nil {
			return e
		}
		err = (&pluginjobs.Queue{Backend: backend}).CancelRestored(ctx, time.Now().UTC())
	}
	if err != nil {
		return err
	}
	return printJSON(map[string]any{"ok": true, "active_tasks": "cancelled", "event_receipts": "cancelled", "idempotency_history": "retained"})
}
