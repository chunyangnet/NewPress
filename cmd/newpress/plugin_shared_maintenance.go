package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/pluginshared"
)

const sharedMaintenanceUsage = `usage: newpress plugin maintain-shared-records <inspect|quarantines|quarantine|repair>
  inspect|quarantines [--type authority|content_effect|webhook_intent] [--plugin SLUG] [--job ID] [--limit 1-1000] [--offset N]
  quarantine --type TYPE --reference SHA256 --request-key KEY --operator NAME --reason TEXT --confirm-all-workers-stopped
  repair --id ID --operator NAME --reason TEXT --confirm-all-workers-stopped [--replacement FILE --replacement-sha256 SHA256 | --keep-reference SHA256]
PostgreSQL only. Stop every instance before mutations; original evidence is permanently retained.`

func maintainSharedPluginRecords(arguments []string) error {
	usage := errors.New(sharedMaintenanceUsage)
	if len(arguments) == 0 {
		return usage
	}
	action := arguments[0]
	allowed := map[string]bool{}
	switch action {
	case "inspect", "quarantines":
		allowed = map[string]bool{"type": true, "plugin": true, "job": true, "limit": true, "offset": true}
	case "quarantine":
		allowed = map[string]bool{"type": true, "reference": true, "request-key": true, "operator": true, "reason": true, "confirm-all-workers-stopped": true}
	case "repair":
		allowed = map[string]bool{"id": true, "operator": true, "reason": true, "confirm-all-workers-stopped": true, "replacement": true, "replacement-sha256": true, "keep-reference": true}
	default:
		return usage
	}
	f := flag.NewFlagSet("maintain-shared-records", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	kind := f.String("type", "", "record type")
	slug := f.String("plugin", "", "plugin slug")
	job := f.String("job", "", "logical job ID")
	limit := f.Int("limit", 100, "page size")
	offset := f.Int("offset", 0, "page offset")
	reference := f.String("reference", "", "original reference")
	key := f.String("request-key", "", "unique capture command")
	operator := f.String("operator", "", "local operator declaration")
	reason := f.String("reason", "", "audited reason")
	stopped := f.Bool("confirm-all-workers-stopped", false, "all instances stopped")
	id := f.String("id", "", "quarantine ID")
	replacement := f.String("replacement", "", "verified original JSON")
	digest := f.String("replacement-sha256", "", "exact original checksum")
	keep := f.String("keep-reference", "", "identical retained record")
	if err := f.Parse(arguments[1:]); err != nil || f.NArg() != 0 {
		return usage
	}
	invalid := false
	f.Visit(func(f *flag.Flag) {
		if !allowed[f.Name] {
			invalid = true
		}
	})
	if invalid {
		return usage
	}
	q := pluginshared.Query{Type: *kind, Plugin: *slug, JobID: *job, Limit: *limit, Offset: *offset}
	capture := pluginshared.CaptureRequest{Type: *kind, Reference: *reference, Key: *key, Operator: *operator, Reason: *reason}
	repair := pluginshared.RepairRequest{ID: *id, Operator: *operator, Reason: *reason, KeepReference: *keep}
	var err error
	switch action {
	case "inspect", "quarantines":
		if *limit < 1 {
			return usage
		}
		q, err = pluginshared.NormalizeQuery(q)
	case "quarantine":
		if !*stopped {
			return usage
		}
		err = pluginshared.ValidateCapture(capture)
	case "repair":
		if !*stopped || (*replacement == "") != (*digest == "") || *keep != "" && *replacement != "" {
			return usage
		}
		if *replacement != "" {
			repair.Replacement, err = readPluginRepairReplacement(*replacement, *digest)
			if err != nil {
				return err
			}
			repair.ReplacementDigest = *digest
		}
		err = pluginshared.ValidateRepair(repair)
	}
	if err != nil {
		return usage
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.DatabaseURL == "" {
		return errors.New("shared record maintenance requires PostgreSQL; file storage was not changed")
	}
	locks, err := dirlock.Acquire(cfg.DataDir, cfg.ThemesDir, cfg.PluginsDir)
	if err != nil {
		return err
	}
	defer locks.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := database.OpenWithPoolLimits(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := database.NewPluginJobRepository(pool)
	switch action {
	case "inspect":
		page, err := store.InspectSharedRecords(ctx, q)
		if err != nil {
			return err
		}
		if err = printJSON(page); err != nil {
			return err
		}
		if page.Fatal != "" {
			return errors.New("shared plugin evidence cannot be verified; retain it and use a complete verified backup")
		}
		return nil
	case "quarantines":
		page, err := store.ListSharedQuarantines(ctx, q)
		if err != nil {
			return err
		}
		return printJSON(page)
	case "quarantine":
		result, err := store.QuarantineSharedRecord(ctx, capture, time.Now().UTC())
		if err != nil {
			return err
		}
		return printJSON(result)
	default:
		result, err := store.RepairSharedRecord(ctx, repair, time.Now().UTC())
		if err != nil {
			return err
		}
		return printJSON(result)
	}
}
