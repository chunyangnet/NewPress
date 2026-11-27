package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/packagefs"
)

const snippetMaintenanceUsage = `usage: newpress snippet records <inspect|review|export-repair|quarantines|quarantine|repair>
  inspect [--file content.json] [--limit 1-100] [--offset N]
  review (--snippet-id ID | --editorial-key SHA256)
  export-repair (--snippet-id ID | --editorial-key SHA256) --output NEW_FILE
  quarantines [--limit 1-100] [--offset N]
  quarantine (--snippet-id ID | --editorial-key SHA256) --reference SHA256 --request-key SHA256 --operator NAME --reason TEXT --confirm-all-instances-stopped
  repair --id ID --replacement FILE --replacement-sha256 SHA256 --operator NAME --reason TEXT --confirm-all-instances-stopped --confirm-source-verified
All commands accept --file content.json to select a stopped local site. Otherwise use configured PostgreSQL or DATA_DIR/content.json.
Export contains private original records and requires a healthy source. PostgreSQL and file repair documents have distinct kinds.
Stop every application instance before mutations. Canonical rows and quarantine evidence are retained.
Unresolved quarantine blocks snippet reads/writes, editorial submissions and media-reference traversal in this application version.
File mode retains evidence in content schema 3 and refuses to open the entire repository while any quarantine is unresolved.
Snippet repair forces withdrawal. A checksum binds supplied bytes; the operator must independently verify their source.`

func readSnippetRepairFile(path, expected string) ([]byte, error) {
	if len(expected) != 64 || strings.Trim(expected, "0123456789abcdef") != "" {
		return nil, database.ErrSnippetMaintenanceInput
	}
	f, info, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.Size() < 1 || info.Size() > database.MaxSnippetRepairBytes {
		return nil, database.ErrSnippetMaintenanceInput
	}
	raw, err := io.ReadAll(io.LimitReader(f, database.MaxSnippetRepairBytes+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	current, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, after) || !os.SameFile(info, current) || info.Size() != int64(len(raw)) || after.Size() != info.Size() || current.Size() != info.Size() ||
		!info.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(current.ModTime()) {
		return nil, database.ErrSnippetMaintenanceConflict
	}
	h := sha256.Sum256(raw)
	if hex.EncodeToString(h[:]) != expected {
		return nil, errors.New("replacement checksum does not match; no repair was applied")
	}
	return raw, nil
}

func writeSnippetRepairFile(path string, raw []byte) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir, err := packagefs.LstatNoSymlink(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !dir.IsDir() {
		return errors.New("repair export requires an existing output directory")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	complete := false
	defer func() {
		f.Close()
		if !complete {
			if current, e := packagefs.LstatNoSymlink(path); e == nil && os.SameFile(info, current) {
				_ = os.Remove(path)
			}
		}
	}()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	current, err := packagefs.LstatNoSymlink(path)
	if err != nil || !os.SameFile(info, current) || current.Size() != int64(len(raw)) {
		return database.ErrSnippetMaintenanceConflict
	}
	complete = true
	h := sha256.Sum256(raw)
	return printJSON(struct {
		Path           string `json:"path"`
		SHA256         string `json:"sha256"`
		Bytes          int    `json:"bytes"`
		PrivateRecords bool   `json:"contains_private_records"`
	}{path, hex.EncodeToString(h[:]), len(raw), true})
}

func maintainSnippetRecordsCommand(arguments []string) error {
	usage := errors.New(snippetMaintenanceUsage)
	if len(arguments) == 0 {
		return usage
	}
	action := arguments[0]
	allowed := map[string]bool{}
	switch action {
	case "review":
		allowed = map[string]bool{"snippet-id": true, "editorial-key": true}
	case "export-repair":
		allowed = map[string]bool{"snippet-id": true, "editorial-key": true, "output": true}
	case "quarantines":
		allowed = map[string]bool{"limit": true, "offset": true}
	case "quarantine":
		allowed = map[string]bool{"snippet-id": true, "editorial-key": true, "reference": true, "request-key": true, "operator": true, "reason": true, "confirm-all-instances-stopped": true}
	case "repair":
		allowed = map[string]bool{"id": true, "replacement": true, "replacement-sha256": true, "operator": true, "reason": true, "confirm-all-instances-stopped": true, "confirm-source-verified": true}
	default:
		return usage
	}
	f := flag.NewFlagSet("snippet records "+action, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	file := f.String("file", "", "explicit stopped content snapshot")
	allowed["file"] = true
	target := database.SnippetMaintenanceTarget{}
	f.Int64Var(&target.SnippetID, "snippet-id", 0, "target snippet")
	f.StringVar(&target.EditorialKey, "editorial-key", "", "common receipt key hash")
	output := f.String("output", "", "new private repair export")
	query := content.SnippetInspectionQuery{}
	f.IntVar(&query.Limit, "limit", 100, "summary page size")
	f.IntVar(&query.Offset, "offset", 0, "summary offset")
	capture := database.SnippetCaptureRequest{}
	f.StringVar(&capture.Reference, "reference", "", "review reference")
	f.StringVar(&capture.RequestKey, "request-key", "", "capture request identity")
	operator := f.String("operator", "", "local operator declaration")
	reason := f.String("reason", "", "audited reason")
	stopped := f.Bool("confirm-all-instances-stopped", false, "all application instances stopped")
	verified := f.Bool("confirm-source-verified", false, "replacement source independently verified")
	repair := database.SnippetRepairRequest{}
	f.StringVar(&repair.ID, "id", "", "quarantine ID")
	f.StringVar(&repair.ReplacementSHA256, "replacement-sha256", "", "exact replacement digest")
	replacement := f.String("replacement", "", "verified replacement document")
	if err := f.Parse(arguments[1:]); err != nil || f.NArg() != 0 {
		return usage
	}
	invalid, explicitFile := false, false
	f.Visit(func(f *flag.Flag) {
		invalid = invalid || !allowed[f.Name]
		explicitFile = explicitFile || f.Name == "file"
	})
	if invalid || explicitFile && strings.TrimSpace(*file) == "" {
		return usage
	}
	capture.Target, capture.Operator, capture.Reason = target, *operator, *reason
	repair.Operator, repair.Reason = *operator, *reason
	switch action {
	case "review", "export-repair":
		if target.Validate() != nil || action == "export-repair" && strings.TrimSpace(*output) == "" {
			return usage
		}
	case "quarantines":
		if query.Validate() != nil {
			return usage
		}
	case "quarantine":
		if !*stopped || capture.Validate() != nil {
			return usage
		}
	case "repair":
		if !*stopped || !*verified || strings.TrimSpace(*replacement) == "" {
			return usage
		}
		var err error
		repair.Replacement, err = readSnippetRepairFile(*replacement, repair.ReplacementSHA256)
		if err != nil {
			return err
		}
		if repair.Validate() != nil {
			return usage
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if explicitFile {
		return maintainSnippetFileCommand(ctx, action, *file, *output, target, query, capture, repair)
	}
	cfg, err := config.LoadDatabase()
	if err != nil {
		return err
	}
	if cfg.DatabaseURL == "" {
		dataDir := strings.TrimSpace(os.Getenv("NEWPRESS_DATA_DIR"))
		if dataDir == "" {
			dataDir = "./var"
		}
		return maintainSnippetFileCommand(ctx, action, filepath.Join(dataDir, "content.json"), *output, target, query, capture, repair)
	}
	pool, err := database.OpenWithPoolLimits(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	switch action {
	case "review":
		report, err := database.ReviewSnippetMaintenance(ctx, pool, target)
		if err != nil {
			return err
		}
		if err = printJSON(report); err != nil {
			return err
		}
		if !report.Healthy {
			return errors.New("selected records have faults; original records were not changed")
		}
		return nil
	case "export-repair":
		raw, err := database.ExportSnippetRepair(ctx, pool, target)
		if err != nil {
			return err
		}
		return writeSnippetRepairFile(*output, raw)
	case "quarantines":
		page, err := database.ListSnippetQuarantines(ctx, pool, query)
		if err != nil {
			return err
		}
		return printJSON(page)
	case "quarantine":
		result, err := database.QuarantineSnippetRecords(ctx, pool, capture, time.Now().UTC())
		if err != nil {
			return err
		}
		return printJSON(result)
	default:
		result, err := database.RepairSnippetRecords(ctx, pool, repair, time.Now().UTC())
		if err != nil {
			return err
		}
		return printJSON(result)
	}
}

func maintainSnippetFileCommand(ctx context.Context, action, file, output string, target database.SnippetMaintenanceTarget, query content.SnippetInspectionQuery, capture database.SnippetCaptureRequest, repair database.SnippetRepairRequest) error {
	path, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	info, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("snippet maintenance requires an existing regular content file")
	}
	locks, err := dirlock.Acquire(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer locks.Close()
	selected := content.SnippetFileTarget{SnippetID: target.SnippetID, EditorialKey: target.EditorialKey}
	switch action {
	case "review":
		report, err := content.ReviewSnippetFileMaintenance(ctx, path, selected)
		if err != nil {
			return err
		}
		if err = printJSON(report); err != nil {
			return err
		}
		if !report.Healthy {
			return errors.New("selected file records have faults; original records were not changed")
		}
		return nil
	case "export-repair":
		raw, err := content.ExportSnippetFileRepair(ctx, path, selected)
		if err != nil {
			return err
		}
		return writeSnippetRepairFile(output, raw)
	case "quarantines":
		page, err := content.ListSnippetFileQuarantines(ctx, path, query)
		if err != nil {
			return err
		}
		return printJSON(page)
	case "quarantine":
		result, err := content.QuarantineSnippetFile(ctx, path, content.SnippetFileCaptureRequest{Target: selected, Reference: capture.Reference, RequestKey: capture.RequestKey, Operator: capture.Operator, Reason: capture.Reason}, time.Now().UTC())
		if err != nil {
			return err
		}
		return printJSON(result)
	case "repair":
		result, err := content.RepairSnippetFile(ctx, path, content.SnippetFileRepairRequest{ID: repair.ID, Operator: repair.Operator, Reason: repair.Reason, Replacement: repair.Replacement, ReplacementSHA256: repair.ReplacementSHA256}, time.Now().UTC())
		if err != nil {
			return err
		}
		return printJSON(result)
	}
	return errors.New(snippetMaintenanceUsage)
}
