package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/app"
	"newpress.local/newpress/internal/backup"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/writingtemplate"
	"newpress.local/newpress/internal/writingtemplate/testkit"
)

func TestWritingTemplateCapacityExecutableBackupRestore(t *testing.T) {
	binary := os.Getenv("NEWPRESS_TEST_BINARY")
	if binary == "" {
		t.Skip("requires ordinary application binary")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	values := map[string]string{
		"NEWPRESS_DATA_DIR": filepath.Join(root, "data"), "NEWPRESS_THEMES_DIR": filepath.Join(root, "themes"),
		"NEWPRESS_PLUGINS_DIR": filepath.Join(root, "plugins"), "NEWPRESS_ACTIVE_THEME": "template-recovery",
		"NEWPRESS_DATABASE_URL": "", "NEWPRESS_SEED_DEMO": "false", "NEWPRESS_ADMIN_PASSWORD_HASH": "",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	for name, body := range map[string]string{
		"theme.json":            `{"schema":1,"slug":"template-recovery","name":"Template recovery","version":"1.0.0","engine":"go-template/v1","view":"public/v1","templates":{"home":"home.html","single":"single.html"}}`,
		"templates/base.html":   `{{define "base"}}{{template "content" .}}{{end}}`,
		"templates/home.html":   `{{define "content"}}home{{end}}`,
		"templates/single.html": `{{define "content"}}single{{end}}`,
	} {
		path := filepath.Join(values["NEWPRESS_THEMES_DIR"], "template-recovery", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	application.Close()
	f := testkit.Capacity(t, "bytes")
	storage, err := writingtemplate.NewFileStorage(filepath.Join(cfg.DataDir, "writing-templates.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.Write(ctx, sha256.Sum256(nil), f.Raw); err != nil {
		t.Fatal(err)
	}
	environment := func(overrides map[string]string) []string {
		var env []string
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "NEWPRESS_") {
				env = append(env, entry)
			}
		}
		for key, value := range overrides {
			env = append(env, key+"="+value)
		}
		return env
	}
	call := func(env map[string]string, args ...string) backup.Report {
		t.Helper()
		command := exec.CommandContext(ctx, binary, append([]string{"backup"}, args...)...)
		command.Env, command.Dir = environment(env), root
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("backup %s: %v %.2048s", args[0], err, stderr.Bytes())
		}
		var result backup.Report
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	archive := filepath.Join(root, "capacity.zip")
	created := call(values, "create", archive)
	if !created.Verified || !created.ApplicationValidated {
		t.Fatal("backup did not validate application", created)
	}
	ignored := map[string]string{"NEWPRESS_DATA_DIR": filepath.Join(root, "must-remain-absent"), "NEWPRESS_ADDR": "invalid"}
	inspected := call(ignored, "inspect", archive)
	if inspected.ArchiveSHA256 != created.ArchiveSHA256 {
		t.Fatal("inspect digest mismatch")
	}
	restored := call(ignored, "restore", archive, "--destination", filepath.Join(root, "restored"))
	if !restored.ApplicationValidated {
		t.Fatal("restored application not validated")
	}
	path := filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "writing-templates.json")
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(f.Raw, raw) {
		t.Fatal("actual CLI restore changed template bytes", err)
	}
	restoredStore, err := writingtemplate.NewFileStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := writingtemplate.New(ctx, restoredStore)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.Apply(ctx, f.Create); err != nil || !replay.Replayed || replay.ID != f.ID || replay.Version != 1 {
		t.Fatal("CLI restoration lost original receipt", replay, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	restored.Environment["NEWPRESS_ADDR"] = address
	restored.Environment["NEWPRESS_SEED_DEMO"] = "false"
	logPath := filepath.Join(root, "restored-serve.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	node := &clusterHTTPNode{name: "template-restored", root: root, base: "http://" + address, client: &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}}
	node.cmd = exec.CommandContext(ctx, binary, "serve")
	node.cmd.Env, node.cmd.Dir = environment(restored.Environment), root
	node.cmd.Stdout, node.cmd.Stderr = log, log
	if err = node.cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	node.done = make(chan struct{})
	go func() { _ = node.cmd.Wait(); _ = log.Close(); close(node.done) }()
	defer node.stop(t)
	clusterEventually(t, 30*time.Second, "restored capacity app ready", func() bool {
		select {
		case <-node.done:
			body, _ := os.ReadFile(logPath)
			t.Fatalf("restored app exited %.2048s", body)
		default:
		}
		status, _, err := node.request("GET", "/health/ready", nil)
		return err == nil && status == http.StatusOK
	})
	node.stop(t)
	for _, source := range []string{filepath.Join(cfg.DataDir, "writing-templates.json"), path} {
		body, err := os.ReadFile(source)
		if err != nil || !bytes.Equal(body, f.Raw) {
			t.Fatal("startup/replay changed template bytes", err)
		}
	}
	if _, err := os.Stat(ignored["NEWPRESS_DATA_DIR"]); !os.IsNotExist(err) {
		t.Fatal("inspect/restore touched unrelated directory", err)
	}
	t.Logf("actual application create/inspect/restore, restored serve readiness and original request replay; constructed library bytes=%d, no real model or sends", len(f.Raw))
}
