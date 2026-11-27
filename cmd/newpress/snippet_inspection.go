package main

import (
	"context"
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

const snippetInspectionUsage = `usage: newpress snippet records inspect [--file content.json] [--limit 1-100] [--offset 0-1000000]
Read-only snippet metadata/history/update-receipt and common editorial-receipt inspection.
Default issue page size: 100. Every page scans one bounded source snapshot; compare snapshot_sha256 when continuing.
Uses configured PostgreSQL, or NEWPRESS_DATA_DIR/content.json in file mode. --file explicitly selects a local file.
File inspection requires the site's directory lock. No records are repaired, removed, restored or exported.
Exits nonzero for faults, incomplete inspection or execution errors.`

func inspectSnippetCommand(arguments []string) error {
	usage := errors.New(snippetInspectionUsage)
	if len(arguments) == 0 || arguments[0] != "inspect" {
		return maintainSnippetRecordsCommand(arguments)
	}
	f := flag.NewFlagSet("snippet records inspect", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	file := f.String("file", "", "explicit content snapshot")
	q := content.SnippetInspectionQuery{}
	f.IntVar(&q.Limit, "limit", 100, "issues per report")
	f.IntVar(&q.Offset, "offset", 0, "issue offset")
	if err := f.Parse(arguments[1:]); err != nil || f.NArg() != 0 || q.Validate() != nil {
		return usage
	}
	explicitFile := false
	f.Visit(func(flag *flag.Flag) { explicitFile = explicitFile || flag.Name == "file" })
	if explicitFile && strings.TrimSpace(*file) == "" {
		return usage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var report content.SnippetInspectionReport
	if !explicitFile {
		cfg, err := config.LoadDatabase()
		if err != nil {
			return err
		}
		if cfg.DatabaseURL != "" {
			pool, err := database.OpenWithPoolLimits(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
			if err != nil {
				return err
			}
			defer pool.Close()
			report, err = database.InspectSnippetRecords(ctx, pool, q)
			if err != nil {
				return err
			}
			return printSnippetInspection(report)
		}
		dataDir := strings.TrimSpace(os.Getenv("NEWPRESS_DATA_DIR"))
		if dataDir == "" {
			dataDir = "./var"
		}
		*file = filepath.Join(dataDir, "content.json")
	}
	path, err := filepath.Abs(*file)
	if err != nil {
		return err
	}
	info, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("snippet inspection requires an existing regular content file")
	}
	locks, err := dirlock.Acquire(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer locks.Close()
	report, err = content.InspectSnippetFile(ctx, path, q)
	if err != nil {
		return err
	}
	return printSnippetInspection(report)
}

func printSnippetInspection(report content.SnippetInspectionReport) error {
	if err := printJSON(report); err != nil {
		return err
	}
	if !report.Complete || !report.Healthy {
		return errors.New("snippet records have faults or could not be fully inspected; retain source evidence and review the report")
	}
	return nil
}
