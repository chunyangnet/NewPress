package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/database"
)

func TestSnippetExecutableMigrateAndBackupRestore(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_PLUGIN_CLI_DATABASE_URL")
	binary, pgBin := os.Getenv("NEWPRESS_TEST_BINARY"), os.Getenv("NEWPRESS_TEST_PG_BIN")
	if dsn == "" || binary == "" || pgBin == "" {
		t.Skip("requires disposable PostgreSQL cluster, application binary and PostgreSQL tools")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("requires disposable loopback cluster")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	base, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	names := []string{fmt.Sprintf("np_snippet_source_%d", time.Now().UnixNano()), fmt.Sprintf("np_snippet_restored_%d", time.Now().UnixNano())}
	withDatabase := func(name string) string { copy := *u; copy.Path = "/" + name; return copy.String() }
	var pools []*pgxpool.Pool
	for _, name := range names {
		if _, err = base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
			defer stop()
			if _, e := base.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); e != nil {
				t.Error(e)
			}
		})
		pool, err := database.Open(ctx, withDatabase(name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		pools = append(pools, pool)
	}
	source, restored := pools[0], pools[1]
	if err = database.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(ctx, `DROP TRIGGER np_posts_invalidate_snippet_refs ON np_posts;DROP FUNCTION np_invalidate_snippet_refs();DROP TABLE np_post_snippet_refs;ALTER TABLE np_posts DROP COLUMN snippet_refs_ready;DELETE FROM newpress_schema_migrations WHERE version='0067_snippet_reference_projection.sql'`); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(ctx, `DROP TABLE np_snippet_receipts,np_snippet_revisions,np_snippets; DELETE FROM newpress_schema_migrations WHERE version='0066_shared_snippets.sql'`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	password, _ := u.User.Password()
	environment := func(overrides map[string]string) []string {
		var result []string
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if _, found := overrides[key]; !found && !strings.HasPrefix(key, "PG") && !strings.HasPrefix(key, "NEWPRESS_") {
				result = append(result, entry)
			}
		}
		for key, value := range overrides {
			result = append(result, key+"="+value)
		}
		return result
	}
	command := func(executable string, env []string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Env, cmd.Dir = env, root
		out, e := cmd.CombinedOutput()
		if e != nil {
			message := string(out)
			if password != "" {
				message = strings.ReplaceAll(message, password, "[redacted]")
			}
			t.Fatalf("%s failed: %v: %s", filepath.Base(executable), e, message)
		}
	}
	migrate := func(name string) {
		command(binary, environment(map[string]string{"NEWPRESS_DATABASE_URL": withDatabase(name), "NEWPRESS_DATA_DIR": filepath.Join(root, name), "NEWPRESS_SEED_DEMO": "false"}), "migrate", "up")
	}
	migrate(names[0])
	migrate(names[0])
	r := database.NewPostRepository(source)
	a := content.SnippetAccess{Identity: "legacy:restore", Actor: "restore", Read: true, Manage: true}
	snippet, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "private history", ContentSource: "<p>retained private history</p>", Visibility: content.SnippetPrivate}, a)
	if err != nil {
		t.Fatal(err)
	}
	snippet, err = r.SaveSnippet(ctx, snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, content.SnippetInput{Title: "shared", ContentSource: "<p>shared first</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	post, err := r.Create(ctx, content.PostInput{Title: "reference article", Slug: "snippet-restored", ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: snippet.CurrentVersion}), Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	snippet, err = r.SaveSnippet(ctx, snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, content.SnippetInput{Title: "shared", ContentSource: "<p>shared second</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	update := content.SnippetUpdate{SnippetID: snippet.ID, SnippetVersion: snippet.Version, Items: []content.SnippetSelection{{ID: post.ID, Version: post.Version}}, RequestKey: strings.Repeat("d", 64)}
	plan, err := r.PreviewSnippetUpdate(ctx, update, a)
	if err != nil {
		t.Fatal(err)
	}
	update.Digest = plan.Digest
	if _, err = r.ApplySnippetUpdate(ctx, update, a); err != nil {
		t.Fatal(err)
	}
	snippet, err = r.WithdrawSnippet(ctx, snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, a)
	if err != nil {
		t.Fatal(err)
	}
	canonical := func(pool *pgxpool.Pool) string {
		t.Helper()
		var state string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_object('snippets',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM np_snippets s),'versions',(SELECT jsonb_agg(to_jsonb(v) ORDER BY snippet_id,version) FROM np_snippet_revisions v),'receipts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY identity,request_key) FROM np_snippet_receipts r),'posts',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM np_posts p),'revisions',(SELECT jsonb_agg(to_jsonb(v) ORDER BY id) FROM np_post_revisions v))::text`).Scan(&state)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := canonical(source)
	verify := func(pool *pgxpool.Pool) {
		t.Helper()
		if canonical(pool) != before {
			t.Fatal("restoration changed reference facts or immutable history")
		}
		repo := database.NewPostRepository(pool)
		resolved, err := repo.ResolveSnippets(ctx, []contenthtml.SnippetKey{{ID: snippet.ID, Version: snippet.CurrentVersion}})
		if err != nil || len(resolved) != 0 {
			t.Fatal("withdrawn reference revived", err)
		}
		replay, err := repo.ApplySnippetUpdate(ctx, update, a)
		if err != nil || !replay.Replayed || replay.Receipt.Items[0].Version != post.Version+1 {
			t.Fatal(replay, err)
		}
		record, err := repo.GetSnippet(ctx, snippet.ID, a)
		if err != nil || len(record.Revisions) != 3 {
			t.Fatal(record, err)
		}
		if canonical(pool) != before {
			t.Fatal("confirmation modified restored state")
		}
	}
	verify(source)
	// The restored copy starts with an explicitly invalidated derived index;
	// actual migrate up must rebuild it while retaining all editorial facts.
	if _, err = source.Exec(ctx, `UPDATE np_posts SET snippet_refs_ready=false;DELETE FROM np_post_snippet_refs`); err != nil {
		t.Fatal(err)
	}
	pgEnv := func(name string) []string {
		return environment(map[string]string{"PGHOST": u.Hostname(), "PGPORT": u.Port(), "PGUSER": u.User.Username(), "PGPASSWORD": password, "PGDATABASE": name})
	}
	archive := filepath.Join(root, "snippets.dump")
	command(filepath.Join(pgBin, "pg_dump"), pgEnv(names[0]), "--format=custom", "--no-owner", "--no-acl", "--file", archive)
	command(filepath.Join(pgBin, "pg_restore"), pgEnv(names[1]), "--no-owner", "--no-acl", "--exit-on-error", "--dbname", names[1], archive)
	migrate(names[1])
	verify(restored)
	var projected int
	if err = restored.QueryRow(ctx, `SELECT count(*) FROM np_post_snippet_refs WHERE post_id=$1 AND snippet_id=$2 AND body_version=$3`, post.ID, snippet.ID, snippet.CurrentVersion).Scan(&projected); err != nil || projected != 1 {
		t.Fatal("restored projection", projected, err)
	}
}
