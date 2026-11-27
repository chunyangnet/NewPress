package database

import (
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
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
)

func TestPostgreSQLWebhookDumpRestoreCloneDoesNotReplay(t *testing.T) {
	bin := os.Getenv("NEWPRESS_TEST_PG_BIN")
	if bin == "" {
		t.Skip("NEWPRESS_TEST_PG_BIN is not set")
	}
	f := newWebhookFixture(t)
	for _, slot := range []string{"one", "two", "three"} {
		f.accept(t, slot)
	}
	f.success(t)
	confirmed := f.claim(t)
	if err := f.webhooks.Finish(f.ctx, confirmed, plugindispatch.Outcome{Code: "http_2xx", HTTPStatus: 204, ResponseDigest: plugineffects.Digest(nil)}); err != nil {
		t.Fatal(err)
	}
	f.claim(t)
	config := f.pool.Config()
	conn := config.ConnConfig
	name := fmt.Sprintf("webhook_clone_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := f.pool.Exec(f.ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	env := append(os.Environ(), "PGHOST="+conn.Host, "PGPORT="+strconv.Itoa(int(conn.Port)), "PGUSER="+conn.User, "PGPASSWORD="+conn.Password, "PGSSLMODE=disable")
	dump := filepath.Join(t.TempDir(), "schema-backup.dump")
	command := exec.CommandContext(f.ctx, filepath.Join(bin, "pg_dump"), "--format=custom", "--no-owner", "--schema="+conn.RuntimeParams["search_path"], "--file="+dump, conn.Database)
	command.Env = env
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("schema dump failed: %v %s", err, out)
	}
	command = exec.CommandContext(f.ctx, filepath.Join(bin, "pg_restore"), "--no-owner", "--exit-on-error", "--dbname="+name, dump)
	command.Env = env
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("schema restore failed: %v %s", err, out)
	}
	config.ConnConfig.Database = name
	clone, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clone.Close)
	webhooks := NewPluginWebhookRepository(clone, true)
	if err = webhooks.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = NewPluginJobRepository(clone).CancelRestored(f.ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := webhooks.Claim(f.ctx); err != nil || ok {
		t.Fatal("clone sent old operation", ok, err)
	}
	p, err := webhooks.List(f.ctx, plugindispatch.Query{})
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]int{}
	for _, i := range p.Items {
		states[i.State]++
	}
	if states["cancelled"] != 1 || states["uncertain"] != 1 || states["http_confirmed"] != 1 {
		t.Fatal(states)
	}
	var original, restored []byte
	if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, confirmed.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err = clone.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, confirmed.ID).Scan(&restored); err != nil || string(original) != string(restored) {
		t.Fatal("clone altered original response fact", err)
	}
	t.Log("actual pg_dump/pg_restore of isolated schema: original operation IDs retained; pending cancelled; sending uncertain; HTTP-confirmed bytes unchanged; zero new claims")
}
