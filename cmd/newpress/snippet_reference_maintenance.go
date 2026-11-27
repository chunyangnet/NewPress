package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/database"
)

const snippetReferencesUsage = `usage: newpress snippet references <inspect|rebuild> [--limit 1-100] [--after ID --through ID]
PostgreSQL only. Default page size: 32. Pass through_id and next_after_id to continue while has_more is true.
inspect is read-only and exits nonzero if this page needs rebuilding.
rebuild commits one page atomically, replacing only derived reference counts. Concurrent changes can require retrying the page.`

func snippetCommand(arguments []string) error {
	if len(arguments) > 0 && arguments[0] == "records" {
		return inspectSnippetCommand(arguments[1:])
	}
	usage := errors.New(snippetReferencesUsage)
	if len(arguments) < 2 || arguments[0] != "references" || (arguments[1] != "inspect" && arguments[1] != "rebuild") {
		return usage
	}
	f := flag.NewFlagSet("snippet references", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	q := database.SnippetReferenceQuery{}
	f.Int64Var(&q.AfterID, "after", 0, "exclusive article ID")
	f.Int64Var(&q.ThroughID, "through", 0, "inclusive article ID ceiling")
	f.IntVar(&q.Limit, "limit", 32, "articles per transaction")
	if err := f.Parse(arguments[2:]); err != nil || f.NArg() != 0 || q.Validate() != nil {
		return usage
	}
	cfg, err := config.LoadDatabase()
	if err != nil {
		return err
	}
	if cfg.DatabaseURL == "" {
		return errors.New("snippet reference maintenance requires PostgreSQL; file indexes are reconstructed when content is opened")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// No app initialization, migrations or local directory writes. PostgreSQL
	// transactions protect this derived-data operation while a site is running.
	pool, err := database.OpenWithPoolLimits(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	var page database.SnippetReferencePage
	if arguments[1] == "inspect" {
		page, err = database.InspectSnippetReferences(ctx, pool, q)
	} else {
		page, err = database.RebuildSnippetReferences(ctx, pool, q)
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01") {
			return errors.New("concurrent changes prevented this page from committing; retry the same after/through page")
		}
		return err
	}
	if err = printJSON(page); err != nil {
		return err
	}
	if arguments[1] == "inspect" && page.NeedsRebuild > 0 {
		return errors.New("this page contains unprepared or inconsistent snippet reference indexes")
	}
	return nil
}
