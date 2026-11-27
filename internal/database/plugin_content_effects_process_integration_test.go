package database

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
)

type processContentEffectFunc func(context.Context, plugineffects.Request) (plugineffects.Result, error)

func (f processContentEffectFunc) WriteContent(ctx context.Context, r plugineffects.Request) (plugineffects.Result, error) {
	return f(ctx, r)
}

func contentEffectProcessArchive(t *testing.T, manifest plugin.Manifest) (string, []byte) {
	t.Helper()
	code, err := os.ReadFile(filepath.Join("..", "plugin", "testdata", "host-bridge.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"plugin.json", raw}, {"plugin.wasm", code}} {
		file, err := w.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "content-effect.zip")
	if err = os.WriteFile(path, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path, code
}

func TestPostgreSQLPluginContentIndependentProcesses(t *testing.T) {
	f := newContentEffectFixture(t, false)
	archive, code := contentEffectProcessArchive(t, f.control.Manifest)
	// Replace only the fixture's synthetic digest with its actual guest bytes.
	f.control.Revision++
	f.control.ModuleDigest = plugineffects.Digest(code)
	if err := f.controls.SaveTaskControl(f.ctx, f.control.Revision-1, f.control); err != nil {
		t.Fatal(err)
	}
	// This seed claim has no guest payload; finish it before making real inputs.
	if err := f.jobs.Change(f.ctx, f.control.Slug, f.request.Authorization.Claim.ID, f.request.Authorization.Claim.Version, "cancel", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	schema := f.pool.Config().ConnConfig.RuntimeParams["search_path"]
	transcript := []any{}
	exits := []any{}
	t.Cleanup(func() {
		if path := os.Getenv("NEWPRESS_CONTENT_EFFECT_REPORT"); path != "" {
			report := map[string]any{"parent_pid": os.Getpid(), "database": f.pool.Config().ConnConfig.Database, "schema": schema, "wasm_sha256": plugineffects.Digest(code), "transcript": transcript, "process_exits": exits, "failed": t.Failed()}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			enc := json.NewEncoder(file)
			enc.SetIndent("", "  ")
			if err = enc.Encode(report); err != nil {
				t.Error(err)
			}
		}
	})
	a := startAuthorityChild(t, schema, filepath.Join(t.TempDir(), "writer-a"), archive)
	b := startAuthorityChild(t, schema, filepath.Join(t.TempDir(), "writer-b"), archive)
	record := func(step string, r authorityProcessReply) authorityProcessReply {
		if r.Schema != schema || r.Database != f.pool.Config().ConnConfig.Database {
			t.Fatal("different content database", r)
		}
		transcript = append(transcript, map[string]any{"step": step, "received_at": time.Now().UTC(), "reply": r})
		t.Logf("content pid=%d sequence=%d phase=%s result=%s", r.PID, r.Sequence, r.Phase, r.ErrorKind)
		return r
	}
	call := func(child *authorityChild, step string, request authorityProcessRequest) authorityProcessReply {
		return record(step, child.receive(t, child.send(t, request), "complete"))
	}
	success := func(r authorityProcessReply) authorityProcessReply {
		if r.ErrorKind != "" {
			t.Fatal("content process failed", r.Operation, r.Error)
		}
		return r
	}
	for _, child := range []*authorityChild{a, b} {
		record("ready", child.receive(t, 0, "ready"))
		success(call(child, "enable", authorityProcessRequest{Operation: "enable"}))
	}
	if a.pid == b.pid || a.pid == os.Getpid() || a.root == b.root {
		t.Fatal("process fixture collapsed")
	}
	newClaim := func(key string, version int64, title string) pluginjobs.Job {
		control := loadPGTaskControl(t, f.ctx, f.controls, f.control.Slug)
		payload, _ := json.Marshal(map[string]any{"method": "content.update", "key": "update-text", "id": f.post.ID, "value": map[string]any{"version": version, "patch": map[string]any{"title": title}}})
		job, err := f.jobs.Enqueue(f.ctx, pluginjobs.Input{Plugin: control.Slug, Epoch: control.Epoch.Token, Kind: "task.execute.v1", Key: key, ActorID: f.user.ID, Payload: payload}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		claim, ok, err := f.jobs.Claim(f.ctx, time.Now().UTC())
		if err != nil || !ok || claim.ID != job.ID {
			t.Fatal("real wasm claim", ok, err)
		}
		return claim
	}
	finish := func(claim pluginjobs.Job) {
		if err := f.jobs.Finish(f.ctx, claim, "", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	claim := newClaim("two-process", 1, "Both processes write once")
	sa := a.send(t, authorityProcessRequest{Operation: "content-execute", Job: claim})
	sb := b.send(t, authorityProcessRequest{Operation: "content-execute", Job: claim})
	ra := success(record("concurrent-a", a.receive(t, sa, "complete")))
	rb := success(record("concurrent-b", b.receive(t, sb, "complete")))
	if ra.Effect == nil || rb.Effect == nil || *ra.Effect != *rb.Effect {
		t.Fatal("independent guests did not confirm one receipt", ra.Effect, rb.Effect)
	}
	f.assertCounts(t, 2, 2, 1, 1, 1)
	control := loadPGTaskControl(t, f.ctx, f.controls, f.control.Slug)
	raw, _ := json.Marshal(control.Manifest)
	update, err := plugineffects.DecodeContentUpdate([]byte(`{"version":1,"patch":{"title":"different request"}}`))
	if err != nil {
		t.Fatal(err)
	}
	conflict := plugineffects.Request{Authorization: plugineffects.Authorization{Claim: claim, ManifestDigest: plugineffects.Digest(raw), ModuleDigest: control.ModuleDigest}, Slot: "update-text", PostID: f.post.ID, Update: update}
	if r := call(b, "same-slot-different-request", authorityProcessRequest{Operation: "content-direct", Effect: &conflict}); r.ErrorKind != "conflict" {
		t.Fatal("independent conflicting request accepted", r.ErrorKind)
	}
	finish(claim)
	claim = newClaim("response-lost", 2, "Commit before response loss")
	if r := call(a, "lost-response", authorityProcessRequest{Operation: "content-lost-response", Job: claim}); r.ErrorKind != "unavailable" || r.Effect != nil {
		t.Fatal("lost response reported success", r.ErrorKind)
	}
	f.assertCounts(t, 3, 3, 2, 2, 2)
	rb = success(call(b, "confirm-lost-response", authorityProcessRequest{Operation: "content-execute", Job: claim}))
	if rb.Effect == nil || rb.Effect.Version != 3 {
		t.Fatal("lost result not recovered")
	}
	f.assertCounts(t, 3, 3, 2, 2, 2)
	finish(claim)
	claim = newClaim("process-crash", 3, "Commit before process crash")
	sequence := a.send(t, authorityProcessRequest{Operation: "content-crash-after-commit", Job: claim})
	committed := record("committed-before-crash", a.receive(t, sequence, "write-committed"))
	select {
	case err := <-a.exited:
		a.closed = true
		if err == nil || a.command.ProcessState.ExitCode() != 23 {
			t.Fatal("crash fixture did not terminate", err)
		}
		exits = append(exits, map[string]any{"pid": a.pid, "exit_code": 23, "intentional_crash": true})
	case <-time.After(5 * time.Second):
		t.Fatal("committed child did not exit")
	}
	f.assertCounts(t, 4, 4, 3, 3, 3)
	previousRoot := a.root
	a = startAuthorityChild(t, schema, previousRoot, archive)
	record("restarted", a.receive(t, 0, "ready"))
	ra = success(call(a, "confirm-after-crash", authorityProcessRequest{Operation: "content-execute", Job: claim}))
	if committed.Effect == nil || ra.Effect == nil || *committed.Effect != *ra.Effect {
		t.Fatal("crash/restart lost original receipt")
	}
	f.assertCounts(t, 4, 4, 3, 3, 3)
	finish(claim)
	claim = newClaim("revoke-before-write", 4, "must not commit")
	sequence = a.send(t, authorityProcessRequest{Operation: "content-paused", Job: claim})
	record("authorized-before-write", a.receive(t, sequence, "write-started"))
	success(call(b, "disable-before-transaction", authorityProcessRequest{Operation: "disable"}))
	if err = a.encoder.Encode(authorityProcessRequest{Sequence: sequence, Operation: "resume"}); err != nil {
		t.Fatal(err)
	}
	if r := record("revoked-write", a.receive(t, sequence, "complete")); r.ErrorKind != "authorization" {
		t.Fatal("transaction admitted revoked package", r.ErrorKind, r.Error)
	}
	f.assertCounts(t, 4, 4, 3, 3, 3)
	for _, child := range []*authorityChild{a, b} {
		success(call(child, "quit", authorityProcessRequest{Operation: "quit"}))
		_ = child.stdin.Close()
		select {
		case err := <-child.exited:
			child.closed = true
			if err != nil {
				t.Fatal("content child exit", err, child.stderr.String())
			}
			exits = append(exits, map[string]any{"pid": child.pid, "exit_code": 0})
		case <-time.After(5 * time.Second):
			t.Fatal("content child did not exit")
		}
	}
	transcript = append(transcript, map[string]any{"final_post_version": 4, "committed_receipts": 3, "content_updates": 3, "scope": fmt.Sprint("real PostgreSQL, real WASM, independent processes; no full HTTP sites")})
}
