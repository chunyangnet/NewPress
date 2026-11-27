package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/pluginjobs"
)

const pluginRecordMaintenanceUsage = `usage: newpress plugin maintain-records <inspect|quarantines|quarantine|repair>
  inspect|quarantines [--type task|task_history|fanout|fanout_history] [--limit 1-1000] [--offset N]
  quarantine --type TYPE --reference SHA256 --request-key KEY --operator NAME --reason TEXT --confirm-all-workers-stopped [--snapshot-sha256 SHA256]
  repair --id QUARANTINE_ID --operator NAME --reason TEXT --confirm-all-workers-stopped [--replacement FILE --replacement-sha256 SHA256 | --keep-reference SHA256] [--snapshot-sha256 SHA256]
File mutations require --snapshot-sha256 from inspect. Stop every application instance before mutations.`

func readPluginRepairReplacement(path, expected string) ([]byte, error) {
	f, info, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.Size() > 512<<10 {
		return nil, pluginjobs.ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(f, (512<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 512<<10 {
		return nil, pluginjobs.ErrInvalid
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	current, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, after) || !os.SameFile(info, current) || info.Size() != int64(len(body)) || after.Size() != info.Size() || current.Size() != info.Size() ||
		!info.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(current.ModTime()) {
		return nil, pluginjobs.ErrConflict
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != expected {
		return nil, errors.New("replacement checksum does not match; no maintenance was applied")
	}
	return body, nil
}

// Operator is a local person's explicit declaration, not an authenticated
// persistent account. Local directory locks cannot verify remote worker state.
func maintainPluginRecords(arguments []string) error {
	usage := errors.New(pluginRecordMaintenanceUsage)
	if len(arguments) == 0 {
		return usage
	}
	action := arguments[0]
	allowed := map[string]bool{}
	switch action {
	case "inspect", "quarantines":
		allowed = map[string]bool{"type": true, "limit": true, "offset": true}
	case "quarantine":
		allowed = map[string]bool{"type": true, "reference": true, "request-key": true, "operator": true, "reason": true, "confirm-all-workers-stopped": true, "snapshot-sha256": true}
	case "repair":
		allowed = map[string]bool{"id": true, "operator": true, "reason": true, "confirm-all-workers-stopped": true, "replacement": true, "replacement-sha256": true, "keep-reference": true, "snapshot-sha256": true}
	default:
		return usage
	}
	flags := flag.NewFlagSet("maintain-records", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	kind := flags.String("type", "", "record class")
	limit := flags.Int("limit", 100, "maximum projected records")
	offset := flags.Int("offset", 0, "page offset")
	reference := flags.String("reference", "", "exact original reference")
	requestKey := flags.String("request-key", "", "unique isolation command key")
	operator := flags.String("operator", "", "local operator declaration")
	reason := flags.String("reason", "", "audited justification")
	stopped := flags.Bool("confirm-all-workers-stopped", false, "every instance is stopped")
	id := flags.String("id", "", "quarantine event ID")
	replacementPath := flags.String("replacement", "", "verified original record JSON")
	replacementDigest := flags.String("replacement-sha256", "", "verified replacement bytes")
	keepReference := flags.String("keep-reference", "", "existing terminal survivor")
	snapshotDigest := flags.String("snapshot-sha256", "", "inspected file snapshot")
	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
		return usage
	}
	invalid := false
	flags.Visit(func(f *flag.Flag) {
		if !allowed[f.Name] {
			invalid = true
		}
	})
	if invalid {
		return usage
	}
	query := pluginjobs.DiagnosticQuery{Type: pluginjobs.RecordType(*kind), Limit: *limit, Offset: *offset}
	quarantine := pluginjobs.QuarantineRequest{Reference: *reference, Key: *requestKey, Type: pluginjobs.RecordType(*kind), Operator: *operator, Reason: *reason, SnapshotDigest: *snapshotDigest}
	repair := pluginjobs.RepairRequest{ID: *id, Operator: *operator, Reason: *reason, KeepReference: *keepReference, SnapshotDigest: *snapshotDigest}
	var err error
	switch action {
	case "inspect", "quarantines":
		if *limit < 1 {
			return usage
		}
		query, err = pluginjobs.NormalizeDiagnosticQuery(query)
	case "quarantine":
		if !*stopped || *requestKey == "" {
			return usage
		}
		quarantine, err = pluginjobs.PrepareQuarantineRequest(quarantine)
	case "repair":
		if !*stopped || (*replacementPath == "") != (*replacementDigest == "") || *keepReference != "" && *replacementPath != "" {
			return usage
		}
		if *replacementPath != "" {
			repair.Replacement, err = readPluginRepairReplacement(*replacementPath, *replacementDigest)
			if err != nil {
				return err
			}
			repair.ReplacementDigest = *replacementDigest
		}
		repair, err = pluginjobs.PrepareRepairRequest(repair)
	}
	if err != nil {
		return usage
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if action == "quarantine" || action == "repair" {
		if cfg.DatabaseURL == "" && *snapshotDigest == "" {
			return errors.New("file maintenance requires --snapshot-sha256 from inspect; no maintenance was applied")
		}
		if cfg.DatabaseURL != "" && *snapshotDigest != "" {
			return errors.New("--snapshot-sha256 applies only to file storage; no maintenance was applied")
		}
	}
	locks, err := dirlock.Acquire(cfg.DataDir, cfg.ThemesDir, cfg.PluginsDir)
	if err != nil {
		return err
	}
	defer locks.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var store pluginjobs.MaintenanceStore
	if cfg.DatabaseURL != "" {
		pool, err := database.OpenWithPoolLimits(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
		if err != nil {
			return err
		}
		defer pool.Close()
		store = database.NewPluginJobRepository(pool)
	} else {
		store, err = pluginjobs.NewFileMaintenance(filepath.Join(cfg.DataDir, "plugin-jobs.json"))
		if err != nil {
			return err
		}
	}
	switch action {
	case "inspect":
		page, err := store.InspectRecords(ctx, query)
		if err != nil {
			return err
		}
		if err = printJSON(page); err != nil {
			return err
		}
		if page.Fatal != "" {
			return errors.New("plugin maintenance inspection cannot trust this snapshot; restore a verified backup")
		}
		return nil
	case "quarantines":
		page, err := store.ListQuarantines(ctx, query)
		if err != nil {
			return err
		}
		return printJSON(page)
	case "quarantine":
		result, err := store.QuarantineRecord(ctx, quarantine, time.Now().UTC())
		if err != nil {
			return err
		}
		return printJSON(result)
	default:
		result, err := store.RepairRecord(ctx, repair, time.Now().UTC())
		if err != nil {
			return err
		}
		return printJSON(result)
	}
}
