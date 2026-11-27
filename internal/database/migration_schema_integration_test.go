package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func assertSchemaConstraints(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for table, names := range map[string][]string{
		"np_posts":    {"np_posts_version_positive", "np_posts_type_valid", "np_posts_status_valid", "np_posts_comment_status_valid"},
		"np_terms":    {"np_terms_version_positive", "np_terms_parent_not_self"},
		"np_users":    {"np_users_version_positive", "np_users_status_valid", "np_users_username_trimmed", "np_users_email_trimmed"},
		"np_roles":    {"np_roles_capabilities_array"},
		"np_comments": {"np_comments_status_valid", "np_comments_version_positive", "np_comments_content_bounded", "np_comments_ip_hash_valid", "np_comments_parent_not_self", "np_comments_edit_metadata_valid", "np_comments_staff_reply_metadata_valid"},
	} {
		for _, name := range names {
			var present bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid=$1::regclass AND conname=$2 AND convalidated)`, table, name).Scan(&present); err != nil {
				t.Fatal(err)
			}
			if !present {
				t.Errorf("missing validated constraint %s on %s in this schema", name, table)
			}
		}
	}
}

func TestPostgreSQLMigrationsIsolateSchemaConstraints(t *testing.T) {
	ctx, first := openSetupIntegrationPool(t)
	if err := Migrate(ctx, first); err != nil {
		t.Fatal("first schema:", err)
	}
	_, second := openSetupIntegrationPool(t)
	for attempt := range 2 {
		if err := Migrate(ctx, second); err != nil {
			t.Fatalf("second schema migration %d: %v", attempt+1, err)
		}
	}
	assertSchemaConstraints(t, ctx, first)
	assertSchemaConstraints(t, ctx, second)
	for _, pool := range []*pgxpool.Pool{first, second} {
		if _, err := pool.Exec(ctx, `INSERT INTO np_posts(type,slug,title) VALUES('flash','schema-flash','快讯')`); err != nil {
			t.Fatal("flash type must be supported by the current schema:", err)
		}
		for _, invalid := range []struct{ sql, constraint string }{
			{`INSERT INTO np_posts(slug,title,version) VALUES('invalid-version','版本',0)`, "np_posts_version_positive"},
			{`INSERT INTO np_posts(slug,title,status) VALUES('invalid-status','状态','unknown')`, "np_posts_status_valid"},
			{`INSERT INTO np_posts(slug,title,type) VALUES('invalid-type','类型','unknown')`, "np_posts_type_valid"},
			{`INSERT INTO np_users(username,email,password_hash,version) VALUES('invalid-version','invalid@example.test','test',0)`, "np_users_version_positive"},
			{`INSERT INTO np_roles(slug,name,capabilities) VALUES('invalid-capabilities','权限','{}')`, "np_roles_capabilities_array"},
		} {
			_, err := pool.Exec(ctx, invalid.sql)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != invalid.constraint {
				t.Errorf("invalid data must fail %s: %v", invalid.constraint, err)
			}
		}
	}
}

func TestPostgreSQLMigrationsEnforceRecordedChecksums(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE newpress_schema_migrations SET checksum=repeat('0',64) WHERE version='0002_content_workflow.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err == nil || !strings.Contains(err.Error(), "checksum changed") {
		t.Fatalf("recorded checksum was not enforced: %v", err)
	}
}

func TestPostgreSQLMigrationsDoNotReuseFallbackSchema(t *testing.T) {
	ctx, first := openSetupIntegrationPool(t)
	if err := Migrate(ctx, first); err != nil {
		t.Fatal(err)
	}
	var firstSchema string
	if err := first.QueryRow(ctx, `SELECT current_schema()`).Scan(&firstSchema); err != nil {
		t.Fatal(err)
	}
	_, second := openSetupIntegrationPool(t)
	var targetSchema string
	if err := second.QueryRow(ctx, `SELECT current_schema()`).Scan(&targetSchema); err != nil {
		t.Fatal(err)
	}
	config := second.Config()
	searchPath := pgx.Identifier{targetSchema}.Sanitize() + "," + pgx.Identifier{firstSchema}.Sanitize()
	config.ConnConfig.RuntimeParams["search_path"] = searchPath
	config.MaxConns = 1
	config.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for range 2 {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"newpress_schema_migrations", "np_posts", "np_currency_settings"} {
		var exists bool
		if err := second.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, pgx.Identifier{targetSchema, name}.Sanitize()).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("migration reported success without creating %s in target schema", name)
		}
	}
	var restored string
	if err := pool.QueryRow(ctx, `SHOW search_path`).Scan(&restored); err != nil || restored != searchPath {
		t.Fatalf("migration leaked connection search_path: %q, err=%v", restored, err)
	}
}
