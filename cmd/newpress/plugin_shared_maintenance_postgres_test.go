package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/pluginshared"
)

func TestPluginSharedMaintenanceExecutableBackupRestore(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_PLUGIN_CLI_DATABASE_URL")
	binary, pgBin := os.Getenv("NEWPRESS_TEST_BINARY"), os.Getenv("NEWPRESS_TEST_PG_BIN")
	if dsn == "" || binary == "" || pgBin == "" {
		t.Skip("requires disposable CLI PostgreSQL cluster, application binary and PostgreSQL tools")
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
	names := []string{fmt.Sprintf("np_shared_source_%d", time.Now().UnixNano()), fmt.Sprintf("np_shared_restored_%d", time.Now().UnixNano())}
	withDatabase := func(name string) string { copy := *u; copy.Path = "/" + name; return copy.String() }
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
	}
	source, err := database.Open(ctx, withDatabase(names[0]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(source.Close)
	if err = database.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	checkContent := seedBackupContentEffect(t, ctx, source)
	store := database.NewPluginJobRepository(source)
	capture := func(kind, key string) pluginshared.QuarantineSummary {
		page, e := store.InspectSharedRecords(ctx, pluginshared.Query{Type: kind})
		if e != nil || page.Fatal != "" || len(page.Items) != 1 || page.Items[0].Fault != "index_mismatch" {
			t.Fatal(page, e)
		}
		q, e := store.QuarantineSharedRecord(ctx, pluginshared.CaptureRequest{Type: kind, Reference: page.Items[0].Reference, Key: key, Operator: "offline-fixture", Reason: "index verified before backup"}, time.Now().UTC())
		if e != nil {
			t.Fatal(e)
		}
		return q
	}
	if _, err = source.Exec(ctx, `UPDATE np_plugin_task_authorizations SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	authority := capture(pluginshared.Authority, "backup-authority")
	if _, err = store.RepairSharedRecord(ctx, pluginshared.RepairRequest{ID: authority.ID, Operator: "offline-fixture", Reason: "retire verified authority before backup"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(ctx, `UPDATE np_plugin_content_effects SET slot='incorrect-index'`); err != nil {
		t.Fatal(err)
	}
	effect := capture(pluginshared.ContentEffect, "backup-receipt")
	evidence := map[string][]byte{}
	readEvidence := func(pool *pgxpool.Pool, id string) []byte {
		var raw []byte
		if e := pool.QueryRow(ctx, `SELECT data FROM np_plugin_shared_quarantines WHERE id=$1`, id).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		return raw
	}
	for _, id := range []string{authority.ID, effect.ID} {
		evidence[id] = readEvidence(source, id)
	}
	root := t.TempDir()
	archive := filepath.Join(root, "shared-records.dump")
	password, _ := u.User.Password()
	environment := func(overrides map[string]string) []string {
		values := []string{}
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if _, found := overrides[key]; !found && !strings.HasPrefix(key, "PG") {
				values = append(values, entry)
			}
		}
		for key, value := range overrides {
			values = append(values, key+"="+value)
		}
		return values
	}
	pgEnv := func(name string) []string {
		return environment(map[string]string{"PGHOST": u.Hostname(), "PGPORT": u.Port(), "PGUSER": u.User.Username(), "PGPASSWORD": password, "PGDATABASE": name})
	}
	command := func(executable string, env []string, args ...string) []byte {
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Env = env
		out, e := cmd.CombinedOutput()
		if e != nil {
			message := string(out)
			if password != "" {
				message = strings.ReplaceAll(message, password, "[redacted]")
			}
			t.Fatalf("%s failed: %v: %s", filepath.Base(executable), e, message)
		}
		return out
	}
	command(filepath.Join(pgBin, "pg_dump"), pgEnv(names[0]), "--format=custom", "--no-owner", "--no-acl", "--file", archive)
	command(filepath.Join(pgBin, "pg_restore"), pgEnv(names[1]), "--no-owner", "--no-acl", "--exit-on-error", "--dbname", names[1], archive)
	restored, err := database.Open(ctx, withDatabase(names[1]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	for id, raw := range evidence {
		if !bytes.Equal(readEvidence(restored, id), raw) {
			t.Fatal("dump/restore changed evidence", id)
		}
	}
	recovered := database.NewPluginJobRepository(restored)
	if _, _, err = recovered.Claim(ctx, time.Now().UTC()); !errors.Is(err, pluginshared.ErrQuarantined) {
		t.Fatal("restored unresolved receipt failed to block work", err)
	}
	cliEnv := environment(map[string]string{"NEWPRESS_DATA_DIR": filepath.Join(root, "data"), "NEWPRESS_THEMES_DIR": filepath.Join(root, "themes"), "NEWPRESS_PLUGINS_DIR": filepath.Join(root, "plugins"), "NEWPRESS_DATABASE_URL": withDatabase(names[1]), "NEWPRESS_DATABASE_URL_FILE": ""})
	command(binary, cliEnv, "plugin", "maintain-shared-records", "inspect", "--type", "authority")
	listed := command(binary, cliEnv, "plugin", "maintain-shared-records", "quarantines")
	var page pluginshared.QuarantinePage
	if json.Unmarshal(listed, &page) != nil || page.Total != 2 {
		t.Fatal("CLI lost restored evidence", string(listed))
	}
	for _, secret := range []string{`"original"`, `"slot"`, `"epoch"`, `"fingerprint"`, `"configuration"`} {
		if bytes.Contains(listed, []byte(secret)) {
			t.Fatal("CLI leaked original", secret)
		}
	}
	repair := []string{"plugin", "maintain-shared-records", "repair", "--id", effect.ID, "--operator", "offline-fixture", "--reason", "verified original receipt after restore", "--confirm-all-workers-stopped"}
	first := command(binary, cliEnv, repair...)
	if !bytes.Equal(first, command(binary, cliEnv, repair...)) {
		t.Fatal("CLI repair confirmation changed")
	}
	checkContent(restored)
	// Also exercise capture through the actual CLI, with a new command key.
	if _, err = restored.Exec(ctx, `UPDATE np_plugin_content_effects SET slot='second-incorrect-index'`); err != nil {
		t.Fatal(err)
	}
	raw := command(binary, cliEnv, "plugin", "maintain-shared-records", "inspect", "--type", "content_effect")
	var diagnostics pluginshared.Page
	if json.Unmarshal(raw, &diagnostics) != nil || len(diagnostics.Items) != 1 || diagnostics.Items[0].Fault != "index_mismatch" {
		t.Fatal(string(raw))
	}
	args := []string{"plugin", "maintain-shared-records", "quarantine", "--type", "content_effect", "--reference", diagnostics.Items[0].Reference, "--request-key", "after-restore-capture", "--operator", "offline-fixture", "--reason", "verified second index damage", "--confirm-all-workers-stopped"}
	first = command(binary, cliEnv, args...)
	if !bytes.Equal(first, command(binary, cliEnv, args...)) {
		t.Fatal("CLI capture confirmation changed")
	}
	var again pluginshared.QuarantineSummary
	if json.Unmarshal(first, &again) != nil || again.ID == effect.ID {
		t.Fatal("new damage reused old evidence")
	}
	repair[4] = again.ID
	command(binary, cliEnv, repair...)
	retained := map[string][]byte{}
	for _, id := range []string{authority.ID, effect.ID, again.ID} {
		retained[id] = readEvidence(restored, id)
	}
	command(binary, cliEnv, "plugin", "cancel-restored-tasks", "--confirm-all-workers-stopped")
	command(binary, cliEnv, "plugin", "cancel-restored-tasks", "--confirm-all-workers-stopped")
	checkContent(restored)
	for id, raw := range retained {
		if !bytes.Equal(raw, readEvidence(restored, id)) {
			t.Fatal("restore changed repair audit", id)
		}
	}
	for id, raw := range evidence {
		if !bytes.Equal(raw, readEvidence(source, id)) {
			t.Fatal("CLI changed source evidence", id)
		}
	}
	if _, _, err = store.Claim(ctx, time.Now().UTC()); !errors.Is(err, pluginshared.ErrQuarantined) {
		t.Fatal("source evidence gate changed", err)
	}
	t.Log("Actual pg_dump/pg_restore and CLI preserved original bytes, repaired authority and unresolved receipt; explicit receipt repair retained its result and source isolation remained unchanged.")
}
