package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/dirlock"
)

func TestSnippetFileMaintenanceExecutable(t *testing.T) {
	binary := os.Getenv("NEWPRESS_TEST_BINARY")
	if binary == "" {
		t.Skip("requires ordinary application binary")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	source, clone := filepath.Join(root, "source"), filepath.Join(root, "clone")
	path := filepath.Join(source, "content.json")
	r, err := content.NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	a := content.SnippetAccess{Identity: "legacy:file-maintainer", Actor: "fixture", Read: true, Manage: true}
	s, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "private title", ContentSource: "<p>private-file-original</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(ctx, content.PostInput{Title: "file recovery", Slug: "file-recovery", Status: content.StatusPublish, ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: s.CurrentVersion})})
	if err != nil {
		t.Fatal(err)
	}
	draft := content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}
	plan, err := r.PreviewSnippetDraft(ctx, draft, a)
	if err != nil {
		t.Fatal(err)
	}
	draft.Digest, draft.RequestKey = plan.Digest, strings.Repeat("a", 64)
	submission, err := content.NewSnippetDraftSubmission(draft, a)
	if err != nil {
		t.Fatal(err)
	}
	created, err := r.Submit(ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	environment := func(values map[string]string) []string {
		var env []string
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "NEWPRESS_") {
				env = append(env, entry)
			}
		}
		for key, value := range values {
			env = append(env, "NEWPRESS_"+key+"="+value)
		}
		return env
	}
	call := func(file string, want int, result any, args ...string) {
		t.Helper()
		command := append([]string{"snippet", "records"}, args...)
		command = append(command, "--file", file)
		cmd := exec.CommandContext(ctx, binary, command...)
		// Explicit file commands must not parse or connect the configured database.
		cmd.Dir, cmd.Env = root, environment(map[string]string{"DATABASE_URL": "invalid database URL", "DATA_DIR": filepath.Join(root, "must-not-create")})
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != want {
			t.Fatalf("CLI %s: %v %s", args[0], err, stderr.String())
		}
		if bytes.Contains(stdout.Bytes(), []byte("private-file-original")) || bytes.Contains(stdout.Bytes(), []byte("legacy:file-maintainer")) {
			t.Fatal("CLI leaked original private records")
		}
		if result != nil {
			if err = json.Unmarshal(stdout.Bytes(), result); err != nil {
				t.Fatal(err)
			}
		}
	}
	file := filepath.Join(root, "verified.json")
	var exported struct{ SHA256 string }
	call(path, 0, &exported, "export-repair", "--snippet-id", fmt.Sprint(s.ID), "--output", file)
	raw, err := os.ReadFile(file)
	if err != nil || exported.SHA256 != fmt.Sprintf("%x", sha256.Sum256(raw)) {
		t.Fatal("invalid export", err)
	}
	call(path, 1, nil, "export-repair", "--snippet-id", fmt.Sprint(s.ID), "--output", file)
	locks, err := dirlock.Acquire(source)
	if err != nil {
		t.Fatal(err)
	}
	call(path, 1, nil, "review", "--snippet-id", fmt.Sprint(s.ID))
	if err = locks.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(original, &doc); err != nil {
		t.Fatal(err)
	}
	var snippets []content.SnippetRecord
	if err = json.Unmarshal(doc["snippets"], &snippets); err != nil {
		t.Fatal(err)
	}
	snippets[0].Revisions[0].ContentHTML = "damaged historical rendering"
	doc["snippets"], _ = json.Marshal(snippets)
	bad, _ := json.Marshal(doc)
	if err = os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	var review content.SnippetFileMaintenanceReview
	call(path, 1, &review, "review", "--snippet-id", fmt.Sprint(s.ID))
	args := []string{"quarantine", "--snippet-id", fmt.Sprint(s.ID), "--reference", review.Reference, "--request-key", strings.Repeat("b", 64), "--operator", "fixture", "--reason", "preserve original"}
	call(path, 1, nil, args...)
	args = append(args, "--confirm-all-instances-stopped")
	var q content.SnippetFileQuarantineSummary
	call(path, 0, &q, args...)
	quarantined, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	call(path, 0, nil, args...)
	unchanged, _ := os.ReadFile(path)
	if !bytes.Equal(unchanged, quarantined) {
		t.Fatal("capture replay rewrote source")
	}
	if err = os.Mkdir(clone, 0700); err != nil {
		t.Fatal(err)
	}
	clonePath := filepath.Join(clone, "content.json")
	if err = os.WriteFile(clonePath, quarantined, 0600); err != nil {
		t.Fatal(err)
	}
	var page content.SnippetFileQuarantinePage
	call(clonePath, 0, &page, "quarantines")
	if page.Unresolved != 1 || page.Total != 1 {
		t.Fatal(page)
	}
	themes := filepath.Join(root, "themes")
	if err = os.CopyFS(filepath.Join(themes, "starter"), os.DirFS(filepath.Join("..", "..", "themes", "starter"))); err != nil {
		t.Fatal(err)
	}
	serveEnv := func(address string) []string {
		return environment(map[string]string{"ADDR": address, "PUBLIC_URL": "http://" + address, "DATA_DIR": clone, "THEMES_DIR": themes, "PLUGINS_DIR": filepath.Join(root, "plugins"), "UPDATE_DIR": filepath.Join(root, "updates"), "ACTIVE_THEME": "starter", "SEED_DEMO": "false", "AUTO_MIGRATE": "false", "ANALYTICS_ENABLED": "false", "UPDATE_CHECK_INTERVAL": "0", "PLUGIN_WEBHOOKS_ENABLED": "false"})
	}
	failedContext, stopFailed := context.WithTimeout(ctx, 20*time.Second)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	failedAddress := probe.Addr().String()
	_ = probe.Close()
	failed := exec.CommandContext(failedContext, binary, "serve")
	failed.Dir, failed.Env = root, serveEnv(failedAddress)
	out, err := failed.CombinedOutput()
	stopFailed()
	if err == nil || failedContext.Err() == context.DeadlineExceeded || !bytes.Contains(out, []byte("quarantined for offline maintenance")) {
		t.Fatalf("ordinary serve did not reject unresolved file: %v %.1024s", err, out)
	}
	repairArgs := []string{"repair", "--id", q.ID, "--replacement", file, "--replacement-sha256", exported.SHA256, "--operator", "fixture", "--reason", "verified original", "--confirm-all-instances-stopped"}
	call(clonePath, 1, nil, repairArgs...)
	repairArgs = append(repairArgs, "--confirm-source-verified")
	var repaired content.SnippetFileQuarantineSummary
	call(clonePath, 0, &repaired, repairArgs...)
	call(clonePath, 0, nil, repairArgs...)
	if repaired.Repair == nil {
		t.Fatal("missing repair audit")
	}
	rr, err := content.NewFileRepository(clonePath, false)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := rr.Submit(ctx, submission)
	if err != nil || !replay.Replayed || replay.Receipt != created.Receipt {
		t.Fatal("restored draft confirmation changed", err)
	}
	meta, err := rr.GetSnippet(ctx, s.ID, a)
	if err != nil || meta.Visibility != content.SnippetWithdrawn || meta.Version <= s.Version {
		t.Fatal(meta, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	n := &clusterHTTPNode{name: "file-repaired", root: root, base: "http://" + address, client: &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}}
	logPath := filepath.Join(root, "serve.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	n.cmd = exec.CommandContext(ctx, binary, "serve")
	n.cmd.Dir, n.cmd.Env, n.cmd.Stdout, n.cmd.Stderr = root, serveEnv(address), log, log
	if err = n.cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	n.done = make(chan struct{})
	go func() { _ = n.cmd.Wait(); _ = log.Close(); close(n.done) }()
	defer n.stop(t)
	clusterEventually(t, 30*time.Second, "repaired file app readiness", func() bool {
		select {
		case <-n.done:
			raw, _ := os.ReadFile(logPath)
			t.Fatalf("repaired app exited %.1024s", raw)
		default:
		}
		status, _, err := n.request("GET", "/health/ready", nil)
		return err == nil && status == 200
	})
	status, body, err := n.request("GET", "/api/v1/posts?limit=1", nil)
	if err != nil || status != 200 || bytes.Contains(body, []byte("private-file-original")) {
		t.Fatal("repaired public output", status, err)
	}
	n.stop(t)
	call(clonePath, 0, &page, "quarantines")
	if page.Total != 1 || page.Unresolved != 0 || page.Items[0].Repair == nil {
		t.Fatal("audit lost on normal app startup", page)
	}
	unchanged, _ = os.ReadFile(path)
	if !bytes.Equal(unchanged, quarantined) {
		t.Fatal("restored repair changed source file")
	}
	if _, err = os.Stat(filepath.Join(root, "must-not-create")); !os.IsNotExist(err) {
		t.Fatal("maintenance initialized unintended storage")
	}
	t.Log("ordinary CLI, explicit file routing, directory lock, schema-3 copy restore, unresolved serve refusal, repaired serve readiness, withdrawal, permanent draft replay and audit retained")
}
