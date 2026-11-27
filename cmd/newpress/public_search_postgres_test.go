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
	"newpress.local/newpress/internal/database"
)

func TestPublicSearchExecutableMigrateAndBackupRestore(t *testing.T) {
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
	names := []string{fmt.Sprintf("np_search_source_%d", time.Now().UnixNano()), fmt.Sprintf("np_search_restored_%d", time.Now().UnixNano())}
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
	post, err := database.NewPostRepository(source).Create(ctx, content.PostInput{Title: "旧内容", Slug: "search-upgrade", Status: content.StatusPublish, Excerpt: "固定摘要", ContentSource: "<p>独有正<em>文</em>恢复标记</p>"})
	if err != nil {
		t.Fatal(err)
	}
	canonical := func(pool *pgxpool.Pool) string {
		t.Helper()
		var data string
		if e := pool.QueryRow(ctx, `SELECT jsonb_build_object('post',to_jsonb(p)-ARRAY['public_search_body','public_search_title','public_search_excerpt','public_search_author'],'revisions',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM np_post_revisions r WHERE r.post_id=p.id))::text FROM np_posts p WHERE id=$1`, post.ID).Scan(&data); e != nil {
			t.Fatal(e)
		}
		return data
	}
	before := canonical(source)
	if _, err = source.Exec(ctx, `DROP TRIGGER np_posts_invalidate_public_search_body ON np_posts; DROP FUNCTION np_invalidate_public_search_body(); ALTER TABLE np_posts DROP COLUMN public_search_title,DROP COLUMN public_search_excerpt,DROP COLUMN public_search_author,DROP COLUMN public_search_body; ALTER TABLE np_users DROP COLUMN public_search_name; ALTER TABLE np_terms DROP COLUMN public_search_name; DROP FUNCTION np_public_search_normalize_v1(text); DELETE FROM newpress_schema_migrations WHERE version='0065_public_search_body.sql'`); err != nil {
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
	verify := func(pool *pgxpool.Pool) {
		t.Helper()
		if canonical(pool) != before {
			t.Fatal("migration or restore changed canonical content/revisions")
		}
		var projection string
		if e := pool.QueryRow(ctx, `SELECT public_search_body FROM np_posts WHERE id=$1`, post.ID).Scan(&projection); e != nil || projection != "独有正文恢复标记" {
			t.Fatal("CLI did not rebuild projection", projection, e)
		}
		page, e := database.NewPostRepository(pool).SearchPublished(ctx, content.PublishedQuery{Search: "独有正文恢复标记"})
		if e != nil || page.Total != 1 || page.Items[0].ID != post.ID {
			t.Fatal("restored search", page, e)
		}
	}
	migrate(names[0])
	migrate(names[0])
	verify(source)
	// Also prove a backup with a missing derived value can be rebuilt by CLI.
	if _, err = source.Exec(ctx, `UPDATE np_posts SET public_search_body=NULL`); err != nil {
		t.Fatal(err)
	}
	pgEnv := func(name string) []string {
		return environment(map[string]string{"PGHOST": u.Hostname(), "PGPORT": u.Port(), "PGUSER": u.User.Username(), "PGPASSWORD": password, "PGDATABASE": name})
	}
	archive := filepath.Join(root, "search.dump")
	command(filepath.Join(pgBin, "pg_dump"), pgEnv(names[0]), "--format=custom", "--no-owner", "--no-acl", "--file", archive)
	command(filepath.Join(pgBin, "pg_restore"), pgEnv(names[1]), "--no-owner", "--no-acl", "--exit-on-error", "--dbname", names[1], archive)
	migrate(names[1])
	verify(restored)
}
