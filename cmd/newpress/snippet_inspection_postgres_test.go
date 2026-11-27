package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/dirlock"
)

func TestSnippetInspectionExecutable(t *testing.T) {
	dsn, binary := os.Getenv("NEWPRESS_TEST_PLUGIN_CLI_DATABASE_URL"), os.Getenv("NEWPRESS_TEST_BINARY")
	if dsn == "" || binary == "" {
		t.Skip("requires disposable PostgreSQL and application binary")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("requires disposable loopback cluster")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	base, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	schema := fmt.Sprintf("np_snippet_inspect_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := base.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			t.Error(e)
		}
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	pool, err := database.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	a := content.SnippetAccess{Identity: "legacy:private-cli-identity", Actor: "operator", Read: true, Manage: true}
	s, err := database.NewPostRepository(pool).SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "private-cli-title", ContentSource: "<p>private-cli-body</p>", Visibility: content.SnippetPrivate}, a)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	unused := filepath.Join(root, "must-not-create")
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "NEWPRESS_") {
			env = append(env, entry)
		}
	}
	env = append(env, "NEWPRESS_DATABASE_URL="+u.String(), "NEWPRESS_DATA_DIR="+unused)
	command := func(exit int, reportExpected bool, args ...string) content.SnippetInspectionReport {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, append([]string{"snippet", "records", "inspect"}, args...)...)
		cmd.Dir, cmd.Env = root, env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != exit {
			password, _ := u.User.Password()
			t.Fatalf("CLI %v: %v %s", args, err, strings.ReplaceAll(stderr.String(), password, "[redacted]"))
		}
		for _, private := range []string{"private-cli-title", "private-cli-body", a.Identity} {
			if strings.Contains(stdout.String()+stderr.String(), private) {
				t.Fatal("CLI disclosed original data")
			}
		}
		var report content.SnippetInspectionReport
		if reportExpected {
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatal("invalid CLI JSON", err)
			}
		} else if stdout.Len() != 0 {
			t.Fatal("failed execution emitted success report")
		}
		return report
	}
	healthy := command(0, true)
	if !healthy.Healthy || !healthy.Complete || healthy.Storage != "postgresql" || healthy.Totals.Snippets != 1 {
		t.Fatal(healthy)
	}
	if _, err = os.Stat(unused); !os.IsNotExist(err) {
		t.Fatal("PG inspection created local storage")
	}
	if _, err = pool.Exec(ctx, `UPDATE np_snippets SET data=$2 WHERE id=$1`, s.ID, []byte(`{`)); err != nil {
		t.Fatal(err)
	}
	broken := command(1, true, "--limit", "1")
	if broken.Healthy || !broken.Complete || broken.IssueCount != 2 || broken.NextOffset != 1 || broken.SnapshotSHA256 == healthy.SnapshotSHA256 {
		t.Fatal(broken)
	}
	second := command(1, true, "--limit", "1", "--offset", "1")
	if second.SnapshotSHA256 != broken.SnapshotSHA256 || second.NextOffset != -1 || len(second.Items) != 1 {
		t.Fatal(second)
	}
	var retained []byte
	if err = pool.QueryRow(ctx, `SELECT data FROM np_snippets WHERE id=$1`, s.ID).Scan(&retained); err != nil || string(retained) != "{" {
		t.Fatal("CLI rewrote damaged evidence", err)
	}
	file := filepath.Join(root, "file-store", "content.json")
	fr, err := content.NewFileRepository(file, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fr.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "private-cli-title", ContentSource: "<p>private-cli-body</p>", Visibility: content.SnippetPrivate}, a); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(file)
	fileReport := command(0, true, "--file", file)
	if fileReport.Storage != "file" || !fileReport.Healthy {
		t.Fatal("explicit file did not override configured PG", fileReport)
	}
	lock, err := dirlock.Acquire(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	command(1, false, "--file", file)
	lock.Close()
	if err = os.WriteFile(file, []byte(`{"schema":2,"schema":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	fatal := command(1, true, "--file", file)
	if fatal.Complete || fatal.Healthy || fatal.Fatal != "invalid_document_json" || fatal.SnapshotSHA256 == "" {
		t.Fatal(fatal)
	}
	if err = os.WriteFile(file+".bak", original, 0600); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(file)
	command(1, true, "--file", file)
	retainedFile, _ := os.ReadFile(file)
	backup, _ := os.ReadFile(file + ".bak")
	if !bytes.Equal(after, retainedFile) || !bytes.Equal(original, backup) {
		t.Fatal("CLI changed original or rollback file")
	}
	command(1, false, "--offset", "-1")
}
