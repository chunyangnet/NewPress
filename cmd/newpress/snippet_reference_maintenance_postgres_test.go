package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/database"
)

func TestSnippetReferenceMaintenanceExecutable(t *testing.T) {
	dsn, binary := os.Getenv("NEWPRESS_TEST_PLUGIN_CLI_DATABASE_URL"), os.Getenv("NEWPRESS_TEST_BINARY")
	if dsn == "" || binary == "" {
		t.Skip("requires disposable PostgreSQL cluster and application binary")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("requires disposable loopback cluster")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	base, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	schema := fmt.Sprintf("np_snippet_maintenance_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := base.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			t.Error(e)
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	pool, err := database.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	r := database.NewPostRepository(pool)
	var posts []content.Post
	for i := range 2 {
		p, err := r.Create(ctx, content.PostInput{Title: fmt.Sprint("CLI index ", i), ContentSource: strings.Repeat(contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 999, Version: 1}), 2)})
		if err != nil {
			t.Fatal(err)
		}
		posts = append(posts, p)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_post_snippet_refs SET occurrences=7 WHERE post_id=$1;`, posts[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_posts SET snippet_refs_ready=false WHERE id=$1`, posts[1].ID); err != nil {
		t.Fatal(err)
	}
	facts := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('posts',(SELECT jsonb_agg(to_jsonb(p)-'snippet_refs_ready' ORDER BY id) FROM np_posts p),'revisions',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM np_post_revisions r))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := facts()
	root := t.TempDir()
	storage := filepath.Join(root, "must-not-be-created")
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "NEWPRESS_") {
			env = append(env, entry)
		}
	}
	env = append(env, "NEWPRESS_DATABASE_URL="+u.String(), "NEWPRESS_DATA_DIR="+storage, "NEWPRESS_THEMES_DIR="+filepath.Join(storage, "themes"), "NEWPRESS_PLUGINS_DIR="+filepath.Join(storage, "plugins"), "NEWPRESS_SEED_DEMO=false")
	command := func(wantExit int, hasReport bool, args ...string) database.SnippetReferencePage {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, append([]string{"snippet", "references"}, args...)...)
		cmd.Env, cmd.Dir = env, root
		var output, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &stderr
		err := cmd.Run()
		if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != wantExit {
			password, _ := u.User.Password()
			t.Fatalf("CLI exit for %v: %v; %s", args, err, strings.ReplaceAll(stderr.String(), password, "[redacted]"))
		}
		var page database.SnippetReferencePage
		if hasReport {
			if err := json.Unmarshal(output.Bytes(), &page); err != nil {
				t.Fatal("missing JSON report", args, err)
			}
		} else if output.Len() != 0 {
			t.Fatal("failed command returned a success report", args)
		}
		return page
	}
	first := command(1, true, "inspect", "--limit", "1")
	if !first.HasMore || first.ThroughID != posts[1].ID || first.NextAfterID != posts[0].ID || first.Items[0].OccurrenceMismatches != 1 || first.Rebuilt != 0 {
		t.Fatal(first)
	}
	// Inspection must not initialize the unprepared next article as a side effect.
	var ready bool
	var occurrences int
	if err = pool.QueryRow(ctx, `SELECT snippet_refs_ready FROM np_posts WHERE id=$1`, posts[1].ID).Scan(&ready); err != nil || ready {
		t.Fatal(ready, err)
	}
	if err = pool.QueryRow(ctx, `SELECT occurrences FROM np_post_snippet_refs WHERE post_id=$1`, posts[0].ID).Scan(&occurrences); err != nil || occurrences != 7 {
		t.Fatal(occurrences, err)
	}
	through := fmt.Sprint(first.ThroughID)
	if page := command(0, true, "rebuild", "--limit", "1", "--through", through); page.Rebuilt != 1 || !page.HasMore {
		t.Fatal(page)
	}
	continuation := []string{"--after", fmt.Sprint(first.NextAfterID), "--through", through}
	second := command(1, true, append([]string{"inspect"}, continuation...)...)
	if second.Checked != 1 || second.HasMore || second.Items[0].ReadyBefore || second.Items[0].OccurrenceMismatches != 0 {
		t.Fatal(second)
	}
	if page := command(0, true, append([]string{"rebuild"}, continuation...)...); page.Rebuilt != 1 {
		t.Fatal(page)
	}
	if page := command(0, true, "inspect"); page.Checked != 2 || page.NeedsRebuild != 0 {
		t.Fatal(page)
	}
	if page := command(0, true, "rebuild"); page.Rebuilt != 0 {
		t.Fatal(page)
	}
	command(1, false, "rebuild", "--after", "1")
	if facts() != before {
		t.Fatal("actual CLI changed canonical article or revision records")
	}
	// Maintenance must not migrate a missing schema/table as implicit startup.
	if _, err = pool.Exec(ctx, `DROP TABLE np_post_snippet_refs`); err != nil {
		t.Fatal(err)
	}
	command(1, false, "inspect")
	var present bool
	if err = pool.QueryRow(ctx, `SELECT to_regclass('np_post_snippet_refs') IS NOT NULL`).Scan(&present); err != nil || present {
		t.Fatal("CLI implicitly migrated", present, err)
	}
	if _, err = os.Stat(storage); !os.IsNotExist(err) {
		t.Fatal("CLI opened local storage", err)
	}
}
