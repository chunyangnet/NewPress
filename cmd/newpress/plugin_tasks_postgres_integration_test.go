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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/pluginjobs"
)

// Opt-in executable/pg_dump/pg_restore exercise. Both databases are created in
// the disposable loopback cluster supplied by the verification runner.
func TestPluginTaskPostgreSQLExecutableBackupRestoreAndArchive(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if isolated := os.Getenv("NEWPRESS_TEST_PLUGIN_CLI_DATABASE_URL"); isolated != "" {
		dsn = isolated
	}
	binary, pgBin := os.Getenv("NEWPRESS_TEST_BINARY"), os.Getenv("NEWPRESS_TEST_PG_BIN")
	if dsn == "" || binary == "" || pgBin == "" {
		t.Skip("requires disposable PostgreSQL, application binary and PostgreSQL tools")
	}
	baseURL, err := url.Parse(dsn)
	if err != nil || (baseURL.Hostname() != "127.0.0.1" && baseURL.Hostname() != "localhost") || baseURL.User == nil {
		t.Fatal("executable restore exercise requires a disposable loopback cluster")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	base, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal("open disposable cluster", err)
	}
	t.Cleanup(base.Close)
	sourceName := fmt.Sprintf("np_plugin_source_%d", time.Now().UnixNano())
	restoredName := fmt.Sprintf("np_plugin_restore_%d", time.Now().UnixNano())
	for _, name := range []string{sourceName, restoredName} {
		if _, err = base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		name := name
		t.Cleanup(func() {
			// DROP DATABASE forces a cluster-wide checkpoint. Concurrent package
			// tests can make the checkpoint exceed ten seconds even after every
			// connection to this disposable database has been closed.
			cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
			defer stop()
			if _, err := base.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
				t.Error(err)
			}
		})
	}
	withDatabase := func(name string) string {
		u := *baseURL
		u.Path = "/" + name
		return u.String()
	}
	source, err := database.Open(ctx, withDatabase(sourceName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(source.Close)
	if err = database.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	checkContentEffect := seedBackupContentEffect(t, ctx, source)
	r := database.NewPluginJobRepository(source)
	now := time.Now().UTC().Add(-time.Hour)
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "succeeded", ActorID: 1, Payload: json.RawMessage(`{"post_id":1}`)}
	shared := plugin.TaskControl{Schema: 1, Slug: in.Plugin, Revision: 1, ModuleDigest: strings.Repeat("b", 64), Enabled: true,
		Manifest: plugin.Manifest{Schema: 1, Slug: in.Plugin, Name: "Restore authorization fixture", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm",
			Capabilities: []string{"settings.read"}, Hooks: map[string][]string{"task.execute.v1": {"np_handle"}, "event.deliver.v1": {"np_handle"}}},
		Configuration: plugin.Configuration{Version: 1, Values: map[string]any{}, Grants: []string{"settings.read"}},
		Epoch:         plugin.TaskEpoch{Schema: 1, Token: in.Epoch, StartedAt: now.Add(-time.Hour), ActorID: in.ActorID}}
	sourceControls := database.NewPluginTaskControlRepository(source)
	if err := sourceControls.SaveTaskControl(ctx, 0, shared); err != nil {
		t.Fatal("seed shared authorization for executable backup", err)
	}
	inputs := map[string]pluginjobs.Input{}
	jobs := map[string]pluginjobs.Job{}
	var oldClaim pluginjobs.Job
	for _, key := range []string{"succeeded", "cancelled", "running", "pending"} {
		item := in
		item.Key = key
		inputs[key] = item
		j, err := r.Enqueue(ctx, item, now)
		if err != nil {
			t.Fatal(err)
		}
		if key == "succeeded" {
			claim, ok, err := r.Claim(ctx, now)
			if err != nil || !ok || claim.ID != j.ID {
				t.Fatal("seed success claim", ok, err)
			}
			if err = r.Finish(ctx, claim, "", now); err != nil {
				t.Fatal(err)
			}
		}
		if key == "cancelled" {
			if err = r.Change(ctx, j.Plugin, j.ID, j.Version, "cancel", now); err != nil {
				t.Fatal(err)
			}
		}
		if key == "running" {
			claim, ok, err := r.Claim(ctx, now)
			if err != nil || !ok || claim.ID != j.ID {
				t.Fatal("seed running lease", claim.ID, ok, err)
			}
			oldClaim = claim
		}
		jobs[key] = j
	}
	details, err := r.GetReview(ctx, in.Plugin, jobs["cancelled"].ID)
	if err != nil {
		t.Fatal(err)
	}
	reviewedInput := in
	reviewedInput.Key, reviewedInput.Payload = "reviewed-archived", json.RawMessage(`{"reviewed":"new parameters after recovery review"}`)
	reviewedRequest := pluginjobs.ReissueRequest{SourceID: jobs["cancelled"].ID, SourceVersion: details.Task.Version, Input: reviewedInput, Reason: "Checked original business results before reissue", AllowNew: true}
	archivedChild, err := r.Reissue(ctx, reviewedRequest, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Change(ctx, in.Plugin, archivedChild.ID, archivedChild.Version, "cancel", now); err != nil {
		t.Fatal(err)
	}
	pendingRequest := reviewedRequest
	pendingRequest.Input.Key = "reviewed-pending"
	pendingChild, err := r.Reissue(ctx, pendingRequest, now)
	if err != nil {
		t.Fatal(err)
	}
	subs := []pluginjobs.Subscription{{Plugin: in.Plugin, Epoch: in.Epoch, Since: now, ActorID: 1}}
	events := []pluginjobs.Event{
		{Key: "delivered-event", Kind: "content.updated", ObjectID: 1, Version: 1, At: now},
		{Key: "partial-event", Kind: "content.updated", ObjectID: 1, Version: 2, At: now},
	}
	for i, event := range events {
		if _, err = r.FreezeFanout(ctx, event, subs); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err = r.CompleteFanout(ctx, event); err != nil {
				t.Fatal(err)
			}
		}
	}
	if result, err := r.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Fanouts: true, Before: now}, now.Add(time.Second)); err != nil || result.Tasks != 3 || result.Fanouts != 1 {
		t.Fatal("seed archive", result, err)
	}
	maintenanceInput := in
	maintenanceInput.Key = "isolated-extra-task"
	extra, err := r.Enqueue(ctx, maintenanceInput, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(ctx, `UPDATE np_plugin_jobs SET plugin='wrong-reader' WHERE id=$1`, extra.ID); err != nil {
		t.Fatal(err)
	}
	findDiagnostic := func(store *database.PluginJobRepository, kind pluginjobs.RecordType, taskID string) pluginjobs.RecordDiagnostic {
		t.Helper()
		page, err := store.InspectRecords(ctx, pluginjobs.DiagnosticQuery{Type: kind})
		if err != nil || page.Fatal != "" {
			t.Fatal("maintenance inspection", page.Fatal, err)
		}
		for _, item := range page.Items {
			if item.Fault != "" && (taskID == "" || item.TaskID == taskID) {
				return item
			}
		}
		t.Fatal("damaged maintenance record missing")
		return pluginjobs.RecordDiagnostic{}
	}
	damaged := findDiagnostic(r, pluginjobs.RecordTask, extra.ID)
	isolatedTask, err := r.QuarantineRecord(ctx, pluginjobs.QuarantineRequest{Reference: damaged.Reference, Type: damaged.Type, Key: "pre-backup-task-isolation", Operator: "offline-fixture", Reason: "checked SQL index before backup"}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(ctx, `UPDATE np_plugin_fanout_archives SET cancelled=true WHERE event_key=$1`, events[0].Key); err != nil {
		t.Fatal(err)
	}
	damaged = findDiagnostic(r, pluginjobs.RecordFanoutHistory, "")
	isolatedFanout, err := r.QuarantineRecord(ctx, pluginjobs.QuarantineRequest{Reference: damaged.Reference, Type: damaged.Type, Key: "pre-backup-fanout-isolation", Operator: "offline-fixture", Reason: "checked frozen history before backup"}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.RepairRecord(ctx, pluginjobs.RepairRequest{ID: isolatedFanout.ID, Operator: "offline-fixture", Reason: "JSON authority retained before backup"}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	originalEvidence := map[string]pluginjobs.QuarantinedRecord{}
	for _, id := range []string{isolatedTask.ID, isolatedFanout.ID} {
		var raw []byte
		if err = source.QueryRow(ctx, `SELECT data FROM np_plugin_record_quarantines WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var evidence pluginjobs.QuarantinedRecord
		if err = json.Unmarshal(raw, &evidence); err != nil || pluginjobs.ValidateQuarantine(evidence) != nil {
			t.Fatal(err)
		}
		originalEvidence[id] = evidence
	}
	root := t.TempDir()
	archive := filepath.Join(root, "plugin-site.dump")
	password, _ := baseURL.User.Password()
	cleanEnv := func(overrides map[string]string) []string {
		out := []string{}
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if _, replaced := overrides[key]; !replaced && !strings.HasPrefix(key, "PG") {
				out = append(out, entry)
			}
		}
		for key, value := range overrides {
			out = append(out, key+"="+value)
		}
		return out
	}
	pgEnv := func(name string) []string {
		return cleanEnv(map[string]string{"PGHOST": baseURL.Hostname(), "PGPORT": baseURL.Port(), "PGUSER": baseURL.User.Username(), "PGPASSWORD": password, "PGDATABASE": name})
	}
	command := func(executable string, env []string, args ...string) []byte {
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			message := string(out)
			if password != "" {
				message = strings.ReplaceAll(message, password, "[redacted]")
			}
			t.Fatalf("%s failed: %v: %s", filepath.Base(executable), err, message)
		}
		return out
	}
	command(filepath.Join(pgBin, "pg_dump"), pgEnv(sourceName), "--format=custom", "--no-owner", "--no-acl", "--file", archive)
	command(filepath.Join(pgBin, "pg_restore"), pgEnv(restoredName), "--no-owner", "--no-acl", "--exit-on-error", "--dbname", restoredName, archive)
	restored, err := database.Open(ctx, withDatabase(restoredName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	checkContentEffect(restored)
	recovered := database.NewPluginJobRepository(restored)
	restoredControls := database.NewPluginTaskControlRepository(restored)
	clonedControl, found, err := restoredControls.LoadTaskControl(ctx, shared.Slug)
	if err != nil || !found || !reflect.DeepEqual(clonedControl, shared) {
		t.Fatal("dump/restore did not preserve complete shared authorization", found, err)
	}
	if err = recovered.CheckClaim(ctx, oldClaim, now); err != nil {
		t.Fatal("dump/restore did not preserve the original lease before the barrier", err)
	}
	cliEnv := cleanEnv(map[string]string{
		"NEWPRESS_DATA_DIR": filepath.Join(root, "data"), "NEWPRESS_THEMES_DIR": filepath.Join(root, "themes"),
		"NEWPRESS_PLUGINS_DIR": filepath.Join(root, "plugins"), "NEWPRESS_DATABASE_URL": withDatabase(restoredName), "NEWPRESS_DATABASE_URL_FILE": "",
	})
	// Restored private evidence is checked before any repair/cancellation. Run
	// real CLI isolation and repair on an indexed reviewed child, too.
	for id, expected := range originalEvidence {
		var raw []byte
		if err = restored.QueryRow(ctx, `SELECT data FROM np_plugin_record_quarantines WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var actual pluginjobs.QuarantinedRecord
		if err = json.Unmarshal(raw, &actual); err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatal("dump/restore changed private quarantine evidence or audit", err)
		}
	}
	if _, err = restored.Exec(ctx, `UPDATE np_plugin_jobs SET plugin='wrong-reader' WHERE id=$1`, pendingChild.ID); err != nil {
		t.Fatal(err)
	}
	inspection := command(binary, cliEnv, "plugin", "maintain-records", "inspect", "--type", "task")
	var diagnosed pluginjobs.DiagnosticPage
	if err = json.Unmarshal(inspection, &diagnosed); err != nil || diagnosed.Fatal != "" {
		t.Fatal("executable maintenance inspection", err)
	}
	var reference string
	for _, item := range diagnosed.Items {
		if item.TaskID == pendingChild.ID && item.Fault == "index_mismatch" {
			reference = item.Reference
		}
	}
	if reference == "" {
		t.Fatal("executable inspection missed reviewed child index damage")
	}
	quarantineArgs := []string{"plugin", "maintain-records", "quarantine", "--type", "task", "--reference", reference, "--request-key", "restored-child-index", "--operator", "offline-fixture", "--reason", "reviewed child index checked", "--confirm-all-workers-stopped"}
	resultBytes := command(binary, cliEnv, quarantineArgs...)
	var childIsolation pluginjobs.QuarantineSummary
	if err = json.Unmarshal(resultBytes, &childIsolation); err != nil || childIsolation.ID == "" {
		t.Fatal("executable isolation failed", err)
	}
	if repeated := command(binary, cliEnv, quarantineArgs...); !bytes.Equal(resultBytes, repeated) {
		t.Fatal("executable isolation confirmation changed the captured audit")
	}
	repairArgs := []string{"plugin", "maintain-records", "repair", "--id", childIsolation.ID, "--operator", "offline-fixture", "--reason", "reviewed child JSON authority checked", "--confirm-all-workers-stopped"}
	resultBytes = command(binary, cliEnv, repairArgs...)
	if repeated := command(binary, cliEnv, repairArgs...); !bytes.Equal(resultBytes, repeated) {
		t.Fatal("executable repair confirmation repeated a transition")
	}
	command(binary, cliEnv, "plugin", "maintain-records", "repair", "--id", isolatedTask.ID, "--operator", "offline-fixture", "--reason", "restored original JSON authority checked", "--confirm-all-workers-stopped")
	listed := command(binary, cliEnv, "plugin", "maintain-records", "quarantines")
	var quarantinePage pluginjobs.QuarantinePage
	if err = json.Unmarshal(listed, &quarantinePage); err != nil || quarantinePage.Total != 3 {
		t.Fatal("executable quarantine list lost restored/new evidence", quarantinePage.Total, err)
	}
	for _, private := range []string{`"original"`, `"payload"`, `"metadata"`, `"epoch"`, `"actor_id"`, `"fingerprint"`} {
		if bytes.Contains(listed, []byte(private)) || bytes.Contains(inspection, []byte(private)) {
			t.Fatal("executable maintenance output exposed private record fields", private)
		}
	}
	out := command(binary, cliEnv, "plugin", "cancel-restored-tasks", "--confirm-all-workers-stopped")
	var cancellation struct {
		OK bool `json:"ok"`
	}
	if err = json.Unmarshal(out, &cancellation); err != nil || !cancellation.OK {
		t.Fatal("executable cancellation did not return success", err)
	}
	retiredControl, found, err := restoredControls.LoadTaskControl(ctx, shared.Slug)
	if err != nil || !found || retiredControl.Enabled || !retiredControl.Retired || retiredControl.Epoch.Token == shared.Epoch.Token || retiredControl.Epoch.ActorID != 0 || retiredControl.TransitionID != "" || retiredControl.TransitionBytes != 0 || !reflect.DeepEqual(retiredControl.Configuration, shared.Configuration) {
		t.Fatal("executable restore barrier did not retire shared authorization", found, err)
	}
	command(binary, cliEnv, "plugin", "cancel-restored-tasks", "--confirm-all-workers-stopped")
	againControl, found, err := restoredControls.LoadTaskControl(ctx, shared.Slug)
	if err != nil || !found || !reflect.DeepEqual(againControl, retiredControl) {
		t.Fatal("repeated executable restore barrier changed retired authorization", found, err)
	}
	unchangedControl, found, err := sourceControls.LoadTaskControl(ctx, shared.Slug)
	if err != nil || !found || !reflect.DeepEqual(unchangedControl, shared) {
		t.Fatal("executable restore barrier changed source authorization", found, err)
	}
	for key, item := range inputs {
		replay, err := recovered.Enqueue(ctx, item, time.Now().UTC())
		want := "cancelled"
		if key == "succeeded" {
			want = "succeeded"
		}
		if err != nil || replay.Status != want || replay.ID != jobs[key].ID {
			t.Fatal("recovered task facts", key, replay.Status, err)
		}
	}
	if err = recovered.CheckClaim(ctx, oldClaim, now); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("executable barrier did not fence old claims", err)
	}
	for _, reviewed := range []struct {
		request pluginjobs.ReissueRequest
		id      string
	}{{reviewedRequest, archivedChild.ID}, {pendingRequest, pendingChild.ID}} {
		reviewed.request.Input.Epoch, reviewed.request.AllowNew = strings.Repeat("b", 64), false
		receipt, err := recovered.Reissue(ctx, reviewed.request, time.Now().UTC())
		if err != nil || receipt.ID != reviewed.id || receipt.Status != "cancelled" || receipt.Reissue == nil {
			t.Fatal("dump/restore lost or resumed reviewed command", receipt, err)
		}
		details, err := recovered.GetReview(ctx, in.Plugin, reviewed.id)
		if err != nil || details.Provenance == nil || details.Provenance.ReviewerID != 1 || details.Provenance.Reason != reviewedRequest.Reason {
			t.Fatal("dump/restore lost private review provenance", details, err)
		}
	}
	for _, event := range events {
		frozen, found, err := recovered.FindFanout(ctx, event)
		if err != nil || !found || !frozen.Cancelled || !reflect.DeepEqual(frozen.Subscriptions, subs) {
			t.Fatal("restore did not preserve cancelled frozen cohorts", frozen, err)
		}
		if err = pluginjobs.DeliverFrozen(ctx, recovered, recovered, event, []pluginjobs.Subscription{{Plugin: "new-reader", Epoch: strings.Repeat("b", 64), Since: now, ActorID: 2}}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if page, err := recovered.List(ctx, pluginjobs.Query{Plugin: "new-reader"}); err != nil || page.Total != 0 {
		t.Fatal("restored fanout admitted new recipients", err)
	}
	out = command(binary, cliEnv, "plugin", "archive-tasks", "--scope", "all", "--limit", "1000", "--before", time.Now().UTC().Format(time.RFC3339Nano))
	var result pluginjobs.ArchiveResult
	if err = json.Unmarshal(out, &result); err != nil || result.Tasks != 4 || result.Fanouts != 1 {
		t.Fatal("executable archive did not move cancelled recovery records", result, err)
	}
	if page, err := recovered.List(ctx, pluginjobs.Query{Plugin: in.Plugin, Archived: true}); err != nil || page.Total != 7 {
		t.Fatal("executable archive lost restored task history", page.Total, err)
	}
	if original, err := r.Enqueue(ctx, inputs["pending"], time.Now().UTC()); err != nil || original.Status != "pending" {
		t.Fatal("restore CLI changed source database", original.Status, err)
	}
	if err = r.CheckClaim(ctx, oldClaim, now); err != nil {
		t.Fatal("restore CLI fenced the source database", err)
	}
	if details, err := recovered.GetReview(ctx, in.Plugin, pendingChild.ID); err != nil || details.Task.ArchivedAt.IsZero() || details.Provenance == nil || details.Provenance.Reason != pendingRequest.Reason {
		t.Fatal("executable archive lost recovered review", details, err)
	}
	pendingRequest.AllowNew = false
	if original, err := r.Reissue(ctx, pendingRequest, time.Now().UTC()); err != nil || original.ID != pendingChild.ID || original.Status != "pending" {
		t.Fatal("restore CLI changed source reviewed work", original, err)
	}
	for id, expected := range originalEvidence {
		var raw []byte
		if err = restored.QueryRow(ctx, `SELECT data FROM np_plugin_record_quarantines WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var actual pluginjobs.QuarantinedRecord
		if err = json.Unmarshal(raw, &actual); err != nil || !reflect.DeepEqual(actual.Original, expected.Original) || actual.Repair == nil {
			t.Fatal("restore/repair/archive discarded private original evidence", err)
		}
	}
	var sourceRaw []byte
	if err = source.QueryRow(ctx, `SELECT data FROM np_plugin_record_quarantines WHERE id=$1`, isolatedTask.ID).Scan(&sourceRaw); err != nil {
		t.Fatal(err)
	}
	var sourceEvidence pluginjobs.QuarantinedRecord
	if err = json.Unmarshal(sourceRaw, &sourceEvidence); err != nil || !reflect.DeepEqual(sourceEvidence, originalEvidence[isolatedTask.ID]) {
		t.Fatal("restored maintenance modified source database evidence", err)
	}
	checkContentEffect(restored)
	checkContentEffect(source)
	t.Log("Executed application maintenance/cancellation/archive CLI after pg_dump/pg_restore: complete original quarantine evidence and audits retained, reviewed commands cancelled without re-execution, old lease fenced; source database unchanged.")
}
