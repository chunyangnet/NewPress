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
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/pluginshared"
)

func TestPluginWebhookMaintenanceExecutableBackupRestore(t *testing.T) {
	dsn, binary, pgBin := os.Getenv("NEWPRESS_TEST_PLUGIN_CLI_DATABASE_URL"), os.Getenv("NEWPRESS_TEST_BINARY"), os.Getenv("NEWPRESS_TEST_PG_BIN")
	if dsn == "" || binary == "" || pgBin == "" {
		t.Skip("requires disposable CLI PostgreSQL, application binary and PostgreSQL tools")
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
	names := []string{fmt.Sprintf("np_hook_source_%d", time.Now().UnixNano()), fmt.Sprintf("np_hook_clone_%d", time.Now().UnixNano())}
	withDB := func(name string) string { copy := *u; copy.Path = "/" + name; return copy.String() }
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
	source, err := database.Open(ctx, withDB(names[0]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(source.Close)
	if err = database.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	originals := map[string][]byte{}
	states := map[string]string{}
	for _, state := range []string{"pending", "sending", "http_confirmed"} {
		r := plugindispatch.Request{Authorization: plugineffects.Authorization{Claim: pluginjobs.Job{ID: strings.Repeat("a", 64)}, ManifestDigest: strings.Repeat("b", 64), ModuleDigest: strings.Repeat("c", 64)}, Slot: state, URL: "https://example.com/private?secret=retained", Body: json.RawMessage(`{"private":"retained original payload"}`)}
		id, fp, body, e := r.Identity()
		if e != nil {
			t.Fatal(e)
		}
		i := plugindispatch.Intent{Schema: 1, ID: id, JobID: r.Authorization.Claim.ID, SourceFingerprint: strings.Repeat("d", 64), Plugin: "cli-webhook", Kind: "task.execute.v1", Epoch: strings.Repeat("e", 64), ActorID: 7, ManifestDigest: r.Authorization.ManifestDigest, ModuleDigest: r.Authorization.ModuleDigest, Slot: r.Slot, Fingerprint: fp, URL: r.URL, Body: body, Version: 1, State: state, CreatedAt: at, UpdatedAt: at}
		if state != "pending" {
			i.Version = 2
			i.AttemptAt = at
			i.AttemptUntil = at.Add(plugindispatch.AttemptDuration)
			i.AttemptToken = strings.Repeat("f", 64)
		}
		if state == "http_confirmed" {
			i.Version = 3
			i.ResultAt = at
			i.Outcome = plugindispatch.Outcome{Code: "http_2xx", HTTPStatus: 204, ResponseDigest: plugineffects.Digest(nil)}
		}
		if plugindispatch.Validate(i) != nil {
			t.Fatal("invalid synthetic retained intent", state)
		}
		raw, _ := json.Marshal(i)
		originals[id] = raw
		states[id] = state
		slot := i.Slot
		if state != "http_confirmed" {
			slot = "wrong-" + slot
		}
		if _, e = source.Exec(ctx, `INSERT INTO np_plugin_webhook_intents(id,job_id,slot,version,state,data) VALUES($1,$2,$3,$4,$5,$6)`, i.ID, i.JobID, slot, i.Version, i.State, raw); e != nil {
			t.Fatal(e)
		}
	}
	root := t.TempDir()
	password, _ := u.User.Password()
	environment := func(overrides map[string]string) []string {
		result := []string{}
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if _, found := overrides[key]; !found && !strings.HasPrefix(key, "PG") {
				result = append(result, entry)
			}
		}
		for key, value := range overrides {
			result = append(result, key+"="+value)
		}
		return result
	}
	cliEnv := func(db string) []string {
		return environment(map[string]string{"NEWPRESS_DATA_DIR": filepath.Join(root, "data"), "NEWPRESS_THEMES_DIR": filepath.Join(root, "themes"), "NEWPRESS_PLUGINS_DIR": filepath.Join(root, "plugins"), "NEWPRESS_DATABASE_URL": withDB(db), "NEWPRESS_DATABASE_URL_FILE": ""})
	}
	command := func(executable string, env []string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Env = env
		out, e := cmd.CombinedOutput()
		if e != nil {
			message := string(out)
			if password != "" {
				message = strings.ReplaceAll(message, password, "[redacted]")
			}
			t.Fatalf("%s failed: %v %s", filepath.Base(executable), e, message)
		}
		return out
	}
	inspect := command(binary, cliEnv(names[0]), "plugin", "maintain-shared-records", "inspect", "--type", "webhook_intent")
	var page pluginshared.Page
	if json.Unmarshal(inspect, &page) != nil || page.Total != 3 {
		t.Fatal(string(inspect))
	}
	evidence := map[string][]byte{}
	ids := []string{}
	readEvidence := func(pool *pgxpool.Pool, id string) []byte {
		t.Helper()
		var raw []byte
		if e := pool.QueryRow(ctx, `SELECT data FROM np_plugin_shared_quarantines WHERE id=$1`, id).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		return raw
	}
	for _, item := range page.Items {
		if item.Fault == "" {
			continue
		}
		out := command(binary, cliEnv(names[0]), "plugin", "maintain-shared-records", "quarantine", "--type", "webhook_intent", "--reference", item.Reference, "--request-key", item.Reference, "--operator", "cli-operator", "--reason", "verified original before backup", "--confirm-all-workers-stopped")
		var q pluginshared.QuarantineSummary
		if json.Unmarshal(out, &q) != nil || q.ID == "" {
			t.Fatal(string(out))
		}
		ids = append(ids, q.ID)
		evidence[q.ID] = readEvidence(source, q.ID)
	}
	if len(ids) != 2 {
		t.Fatal("expected two isolated attempts")
	}
	archive := filepath.Join(root, "webhook-evidence.dump")
	pgEnv := func(name string) []string {
		return environment(map[string]string{"PGHOST": u.Hostname(), "PGPORT": u.Port(), "PGUSER": u.User.Username(), "PGPASSWORD": password, "PGDATABASE": name})
	}
	command(filepath.Join(pgBin, "pg_dump"), pgEnv(names[0]), "--format=custom", "--no-owner", "--no-acl", "--file", archive)
	command(filepath.Join(pgBin, "pg_restore"), pgEnv(names[1]), "--no-owner", "--no-acl", "--exit-on-error", "--dbname", names[1], archive)
	clone, err := database.Open(ctx, withDB(names[1]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clone.Close)
	for id, raw := range evidence {
		if !bytes.Equal(raw, readEvidence(clone, id)) {
			t.Fatal("dump/restore changed evidence")
		}
	}
	webhooks := database.NewPluginWebhookRepository(clone, true)
	if _, ok, err := webhooks.Claim(ctx); ok || !errors.Is(err, pluginshared.ErrQuarantined) {
		t.Fatal("restored isolation did not block send", ok, err)
	}
	listed := command(binary, cliEnv(names[1]), "plugin", "maintain-shared-records", "quarantines", "--type", "webhook_intent")
	for _, secret := range []string{"retained original payload", "example.com", `"original"`, `"epoch"`, `"fingerprint"`, `"attempt_token"`} {
		if bytes.Contains(inspect, []byte(secret)) || bytes.Contains(listed, []byte(secret)) {
			t.Fatal("CLI exposed retained private data", secret)
		}
	}
	for _, id := range ids {
		args := []string{"plugin", "maintain-shared-records", "repair", "--id", id, "--operator", "cli-operator", "--reason", "verified restored request and attempt", "--confirm-all-workers-stopped"}
		first := command(binary, cliEnv(names[1]), args...)
		if !bytes.Equal(first, command(binary, cliEnv(names[1]), args...)) {
			t.Fatal("repeat CLI repair changed audit")
		}
	}
	if _, ok, err := webhooks.Claim(ctx); err != nil || ok {
		t.Fatal("repaired clone attempted send", ok, err)
	}
	beforeRestore := map[string][]byte{}
	for id, old := range originals {
		var raw []byte
		if err = clone.QueryRow(ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		beforeRestore[id] = raw
		var i plugindispatch.Intent
		if json.Unmarshal(raw, &i) != nil || plugindispatch.Validate(i) != nil {
			t.Fatal("bad repaired intent")
		}
		switch states[id] {
		case "pending":
			if i.State != "cancelled" || i.Outcome.Code != "maintenance" {
				t.Fatal("pending revived")
			}
		case "sending":
			if i.State != "uncertain" || i.Outcome.Code != "maintenance" || i.AttemptToken == "" {
				t.Fatal("attempt lost")
			}
		case "http_confirmed":
			if !bytes.Equal(old, raw) {
				t.Fatal("confirmed fact changed")
			}
		}
	}
	command(binary, cliEnv(names[1]), "plugin", "cancel-restored-tasks", "--confirm-all-workers-stopped")
	for id, before := range beforeRestore {
		var after []byte
		if err = clone.QueryRow(ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, id).Scan(&after); err != nil || !bytes.Equal(before, after) {
			t.Fatal("restore changed terminal", err)
		}
	}
	for id, raw := range evidence {
		if !bytes.Equal(raw, readEvidence(source, id)) {
			t.Fatal("clone maintenance modified source evidence")
		}
	}
	if _, ok, err := webhooks.Claim(ctx); err != nil || ok {
		t.Fatal("restore replayed intent", ok, err)
	}
	t.Log("real executable inspect/quarantine/repair/restore; actual pg_dump/pg_restore; synthetic retained intents; original bytes and identity retained; zero new send claims")
}
