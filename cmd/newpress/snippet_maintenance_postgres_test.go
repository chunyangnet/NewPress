package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/database"
)

func TestSnippetMaintenanceExecutableBackupRestore(t *testing.T) {
	dsn, binary, pgBin := os.Getenv("NEWPRESS_TEST_PLUGIN_CLI_DATABASE_URL"), os.Getenv("NEWPRESS_TEST_BINARY"), os.Getenv("NEWPRESS_TEST_PG_BIN")
	if dsn == "" || binary == "" || pgBin == "" {
		t.Skip("requires disposable PostgreSQL, actual application and backup tools")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("requires disposable loopback cluster")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	base, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	names := []string{fmt.Sprintf("np_repair_source_%d", time.Now().UnixNano()), fmt.Sprintf("np_repair_unresolved_%d", time.Now().UnixNano()), fmt.Sprintf("np_repair_resolved_%d", time.Now().UnixNano())}
	urlFor := func(name string) string { v := *u; v.Path = "/" + name; return v.String() }
	var pools []*pgxpool.Pool
	for _, name := range names {
		if _, err = base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			clean, stop := context.WithTimeout(context.Background(), time.Minute)
			defer stop()
			if _, e := base.Exec(clean, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); e != nil {
				t.Errorf("drop disposable database %s: %v", name, e)
			}
		})
		pool, e := database.Open(ctx, urlFor(name))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(pool.Close)
		pools = append(pools, pool)
	}
	root := t.TempDir()
	unused := filepath.Join(root, "records-must-not-create")
	password, _ := u.User.Password()
	environment := func(values map[string]string) []string {
		result := []string{}
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			upper := strings.ToUpper(key)
			if !strings.HasPrefix(upper, "NEWPRESS_") && !strings.HasPrefix(upper, "PG") {
				result = append(result, entry)
			}
		}
		for key, value := range values {
			result = append(result, key+"="+value)
		}
		return result
	}
	call := func(name string, exit int, result any, args ...string) {
		t.Helper()
		dataDir := unused
		if len(args) > 0 && args[0] == "migrate" {
			dataDir = filepath.Join(root, "migration-storage")
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir, cmd.Env = root, environment(map[string]string{"NEWPRESS_DATABASE_URL": urlFor(name), "NEWPRESS_DATA_DIR": dataDir, "NEWPRESS_SEED_DEMO": "false"})
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != exit {
			t.Fatalf("CLI %v: %v %s", args, err, strings.ReplaceAll(stderr.String(), password, "[redacted]"))
		}
		for _, secret := range []string{"private-cli-history-body", "private-cli-source-title", "legacy:private-cli-maintainer"} {
			if strings.Contains(stdout.String()+stderr.String(), secret) {
				t.Fatal("CLI exposed original private records")
			}
		}
		if result != nil {
			if err := json.Unmarshal(stdout.Bytes(), result); err != nil {
				t.Fatal("invalid CLI report", err)
			}
		} else if exit != 0 && stdout.Len() != 0 {
			t.Fatal("failed command emitted a success report")
		}
	}
	call(names[0], 0, nil, "migrate", "up")
	source, clone := pools[0], pools[1]
	r := database.NewPostRepository(source)
	a := content.SnippetAccess{Identity: "legacy:private-cli-maintainer", Actor: "operator", Read: true, Manage: true}
	s, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "private-cli-source-title", ContentSource: "<p>private-cli-history-body</p>", Visibility: content.SnippetPrivate}, a)
	if err != nil {
		t.Fatal(err)
	}
	s, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "shared", ContentSource: "<p>first shared</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(ctx, content.PostInput{Title: "source", ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: s.CurrentVersion})})
	if err != nil {
		t.Fatal(err)
	}
	s, err = r.SaveSnippet(ctx, s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: "shared", ContentSource: "<p>latest shared</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	upt := content.SnippetUpdate{SnippetID: s.ID, SnippetVersion: s.Version, Items: []content.SnippetSelection{{ID: p.ID, Version: p.Version}}, RequestKey: strings.Repeat("a", 64)}
	plan, err := r.PreviewSnippetUpdate(ctx, upt, a)
	if err != nil {
		t.Fatal(err)
	}
	upt.Digest = plan.Digest
	if _, err = r.ApplySnippetUpdate(ctx, upt, a); err != nil {
		t.Fatal(err)
	}
	p, err = r.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	draftRequest := content.SnippetDraftRequest{PostID: p.ID, Version: p.Version}
	preview, err := r.PreviewSnippetDraft(ctx, draftRequest, a)
	if err != nil {
		t.Fatal(err)
	}
	draftRequest.Digest, draftRequest.RequestKey = preview.Digest, strings.Repeat("b", 64)
	submission, err := content.NewSnippetDraftSubmission(draftRequest, a)
	if err != nil {
		t.Fatal(err)
	}
	created, err := r.Submit(ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	stable := func(pool *pgxpool.Pool) string {
		t.Helper()
		var facts string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'posts',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM np_posts p),
 'revisions',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM np_post_revisions r),
 'autosaves',(SELECT jsonb_agg(to_jsonb(a) ORDER BY post_id,owner_key) FROM np_post_autosaves a),
 'history',(SELECT jsonb_agg(to_jsonb(h) ORDER BY snippet_id,version) FROM np_snippet_revisions h),
 'updates',(SELECT jsonb_agg(to_jsonb(u) ORDER BY identity,request_key) FROM np_snippet_receipts u),
 'editorial',(SELECT jsonb_agg(to_jsonb(e) ORDER BY key_hash) FROM np_content_submissions e))::text`).Scan(&facts); err != nil {
			t.Fatal(err)
		}
		return facts
	}
	goodFacts := stable(source)
	file := filepath.Join(root, "verified-repair.json")
	var exported struct {
		Path, SHA256 string
		Bytes        int
		Private      bool `json:"contains_private_records"`
	}
	call(names[0], 0, &exported, "snippet", "records", "export-repair", "--snippet-id", fmt.Sprint(s.ID), "--output", file)
	raw, err := os.ReadFile(file)
	if err != nil || !exported.Private || exported.Bytes != len(raw) || exported.SHA256 != fmt.Sprintf("%x", sha256.Sum256(raw)) {
		t.Fatal(exported, err)
	}
	call(names[0], 1, nil, "snippet", "records", "export-repair", "--snippet-id", fmt.Sprint(s.ID), "--output", file)
	if _, err = source.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, s.ID, []byte(`{`)); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(ctx, `UPDATE np_snippet_receipts SET data=$1`, []byte(`{"invalid":"private-cli-history-body"}`)); err != nil {
		t.Fatal(err)
	}
	badFacts := stable(source)
	var review database.SnippetMaintenanceReview
	call(names[0], 1, &review, "snippet", "records", "review", "--snippet-id", fmt.Sprint(s.ID))
	captureArgs := []string{"snippet", "records", "quarantine", "--snippet-id", fmt.Sprint(s.ID), "--reference", review.Reference, "--request-key", strings.Repeat("c", 64), "--operator", "operator", "--reason", "retain original damage"}
	call(names[0], 1, nil, captureArgs...)
	captureArgs = append(captureArgs, "--confirm-all-instances-stopped")
	var q database.SnippetQuarantineSummary
	call(names[0], 0, &q, captureArgs...)
	if stable(source) != badFacts {
		t.Fatal("quarantine changed canonical evidence")
	}
	var repeated database.SnippetQuarantineSummary
	call(names[0], 0, &repeated, captureArgs...)
	if repeated.ID != q.ID {
		t.Fatal("capture replay duplicated evidence")
	}
	backup := func(from, to, label string) {
		t.Helper()
		archive := filepath.Join(root, label+".dump")
		for _, step := range []struct {
			exe, db string
			args    []string
		}{
			{"pg_dump", from, []string{"--format=custom", "--no-owner", "--no-acl", "--file", archive}},
			{"pg_restore", to, []string{"--no-owner", "--no-acl", "--exit-on-error", "--dbname", to, archive}},
		} {
			cmd := exec.CommandContext(ctx, filepath.Join(pgBin, step.exe), step.args...)
			cmd.Dir, cmd.Env = root, environment(map[string]string{"PGHOST": u.Hostname(), "PGPORT": u.Port(), "PGUSER": u.User.Username(), "PGPASSWORD": password, "PGDATABASE": step.db})
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("backup tool: %v %s", err, strings.ReplaceAll(string(out), password, "[redacted]"))
			}
		}
	}
	backup(names[0], names[1], "unresolved")
	if stable(clone) != badFacts {
		t.Fatal("dump/restore changed damaged canonical evidence")
	}
	var sourceEvidence, clonedEvidence []byte
	if err = source.QueryRow(ctx, `SELECT data FROM np_snippet_quarantines WHERE id=$1`, q.ID).Scan(&sourceEvidence); err != nil {
		t.Fatal(err)
	}
	if err = clone.QueryRow(ctx, `SELECT data FROM np_snippet_quarantines WHERE id=$1`, q.ID).Scan(&clonedEvidence); err != nil || !bytes.Equal(sourceEvidence, clonedEvidence) {
		t.Fatal("dump/restore changed raw quarantine evidence", err)
	}
	cr := database.NewPostRepository(clone)
	if _, err = cr.FindSubmission(ctx, submission.Key, submission.Fingerprint); !errors.Is(err, content.ErrSnippetMaintenance) {
		t.Fatal("restored unresolved evidence lost gate", err)
	}
	var page database.SnippetQuarantinePage
	call(names[1], 0, &page, "snippet", "records", "quarantines")
	if page.Unresolved != 1 || page.Total != 1 {
		t.Fatal(page)
	}
	repairArgs := []string{"snippet", "records", "repair", "--id", q.ID, "--replacement", file, "--replacement-sha256", exported.SHA256, "--operator", "operator", "--reason", "verified original backup", "--confirm-all-instances-stopped"}
	call(names[1], 1, nil, repairArgs...)
	repairArgs = append(repairArgs, "--confirm-source-verified")
	var repaired database.SnippetQuarantineSummary
	call(names[1], 0, &repaired, repairArgs...)
	call(names[1], 0, &repeated, repairArgs...)
	if repaired.Repair == nil || repeated.Repair == nil || repaired.Repair.SnippetVersion != 4 || !repaired.Repair.At.Equal(repeated.Repair.At) {
		t.Fatal("repair replay changed result")
	}
	if stable(clone) != goodFacts {
		t.Fatal("repair changed retained content, history or receipts")
	}
	if stable(source) != badFacts {
		t.Fatal("clone repair modified source database")
	}
	verify := func(pool *pgxpool.Pool) {
		t.Helper()
		repo := database.NewPostRepository(pool)
		record, e := repo.GetSnippet(ctx, s.ID, a)
		if e != nil || record.Visibility != content.SnippetWithdrawn || record.SharedSince != 0 || record.Version != 4 || len(record.Revisions) != 3 {
			t.Fatal(record.Snippet, e)
		}
		resolved, e := repo.ResolveSnippets(ctx, []contenthtml.SnippetKey{{ID: s.ID, Version: 2}, {ID: s.ID, Version: 3}})
		if e != nil || len(resolved) != 0 {
			t.Fatal("repair or restore revived sharing", e)
		}
		update, e := repo.ApplySnippetUpdate(ctx, upt, a)
		if e != nil || !update.Replayed || update.Receipt.Items[0].Version != p.Version {
			t.Fatal("update replay lost after restore", e)
		}
		draft, e := repo.Submit(ctx, submission)
		if e != nil || !draft.Replayed || draft.Receipt.PostID != created.Receipt.PostID {
			t.Fatal("creation receipt lost after restore", e)
		}
		if stable(pool) != goodFacts {
			t.Fatal("confirmation changed historical facts")
		}
	}
	verify(clone)
	backup(names[1], names[2], "repaired")
	verify(pools[2])
	call(names[2], 0, &page, "snippet", "records", "quarantines")
	if page.Unresolved != 0 || page.Total != 1 || page.Items[0].Repair == nil {
		t.Fatal("repair audit missing after restore", page)
	}
	var report content.SnippetInspectionReport
	call(names[2], 0, &report, "snippet", "records", "inspect")
	if !report.Healthy || !report.Complete {
		t.Fatal(report)
	}
	if _, err = os.Stat(unused); !os.IsNotExist(err) {
		t.Fatal("maintenance initialized local site storage")
	}
}
