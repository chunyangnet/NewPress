package database

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/writingtemplate"
)

func TestPostgreSQLWritingTemplateDumpRestore(t *testing.T) {
	bin := os.Getenv("NEWPRESS_TEST_PG_BIN")
	if bin == "" {
		t.Skip("NEWPRESS_TEST_PG_BIN is not set")
	}
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := NewWritingTemplateStorage(pool)
	service, err := writingtemplate.New(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	d := writingtemplate.Definition{BasePrompt: "rewrite", Title: "克隆恢复模板", Instruction: "保留原稿事实。"}
	create := writingtemplate.Command{Action: "save", Definition: &d, Actor: "user:7", RequestKey: fmt.Sprintf("%064x", 1)}
	first, err := service.Apply(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Apply(ctx, writingtemplate.Command{Action: "archive", ID: first.ID, ExpectedVersion: 1, Actor: "user:7", RequestKey: fmt.Sprintf("%064x", 2)}); err != nil {
		t.Fatal(err)
	}
	original, err := store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	config := pool.Config()
	conn := config.ConnConfig
	name := fmt.Sprintf("writing_templates_clone_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = pool.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// DROP DATABASE may wait for the whole cluster's pending checkpoint.
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := pool.Exec(cleanup, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	env := append(os.Environ(), "PGHOST="+conn.Host, "PGPORT="+strconv.Itoa(int(conn.Port)), "PGUSER="+conn.User, "PGPASSWORD="+conn.Password, "PGSSLMODE=disable")
	dump := filepath.Join(t.TempDir(), "writing-template-schema.dump")
	command := exec.CommandContext(ctx, filepath.Join(bin, "pg_dump"), "--format=custom", "--no-owner", "--schema="+conn.RuntimeParams["search_path"], "--file="+dump, conn.Database)
	command.Env = env
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dump: %v %s", err, out)
	}
	command = exec.CommandContext(ctx, filepath.Join(bin, "pg_restore"), "--no-owner", "--exit-on-error", "--dbname="+name, dump)
	command.Env = env
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restore: %v %s", err, out)
	}
	config.ConnConfig.Database = name
	clone, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clone.Close)
	restoredStore := NewWritingTemplateStorage(clone)
	restored, err := writingtemplate.New(ctx, restoredStore)
	if err != nil {
		t.Fatal(err)
	}
	after, err := restoredStore.Read(ctx)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("restore altered library bytes", err)
	}
	replay, err := restored.Apply(ctx, create)
	if err != nil || !replay.Replayed || replay.ID != first.ID || replay.Version != 1 {
		t.Fatal(replay, err)
	}
	item, err := restored.Get(ctx, first.ID)
	if err != nil || len(item.Revisions) != 2 || !item.Revisions[1].Archived {
		t.Fatal(item, err)
	}
	active, err := restored.Apply(ctx, writingtemplate.Command{Action: "restore", ID: first.ID, ExpectedVersion: 2, RestoreVersion: 1, Actor: "user:7", RequestKey: fmt.Sprintf("%064x", 3)})
	if err != nil || active.Version != 3 {
		t.Fatal(active, err)
	}
	sourceAfter, err := store.Read(ctx)
	if err != nil || !bytes.Equal(original, sourceAfter) {
		t.Fatal("clone modified source", err)
	}
	t.Log("pg_dump/pg_restore retained exact library bytes, identities, history, archive and permanent request replay; clone can append independently")
}
