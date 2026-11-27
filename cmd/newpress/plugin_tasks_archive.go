package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"time"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/pluginjobs"
)

// File CLI operations take the site's directory lock. Online maintenance uses
// the administrator API and the application's existing backend instead.
func archivePluginTasks(arguments []string) error {
	flags := flag.NewFlagSet("archive-tasks", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	beforeText := flags.String("before", "", "UTC or offset RFC3339 cutoff")
	scope := flags.String("scope", "tasks", "tasks, fanouts or all")
	plugin := flags.String("plugin", "", "one plugin; task scope only")
	limit := flags.Int("limit", 100, "maximum records per selected class")
	usage := errors.New("usage: newpress plugin archive-tasks --before RFC3339 [--scope tasks|fanouts|all] [--plugin slug] [--limit 1-1000]")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return usage
	}
	before, err := time.Parse(time.RFC3339Nano, *beforeText)
	if err != nil || *limit < 1 || (*scope != "tasks" && *scope != "fanouts" && *scope != "all") {
		return usage
	}
	now := time.Now().UTC()
	query := pluginjobs.ArchiveQuery{Plugin: *plugin, Before: before, Limit: *limit, Tasks: *scope != "fanouts", Fanouts: *scope != "tasks"}
	if query, err = pluginjobs.NormalizeArchiveQuery(query, now); err != nil {
		return usage
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
	var store pluginjobs.ArchiveStore
	if cfg.DatabaseURL != "" {
		pool, err := database.OpenWithPoolLimits(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
		if err != nil {
			return err
		}
		defer pool.Close()
		store = database.NewPluginJobRepository(pool)
	} else {
		backend, err := pluginjobs.NewFileBackend(filepath.Join(cfg.DataDir, "plugin-jobs.json"))
		if err != nil {
			return err
		}
		store = &pluginjobs.Queue{Backend: backend}
	}
	result, err := store.Archive(ctx, query, now)
	if err != nil {
		return err
	}
	return printJSON(result)
}
