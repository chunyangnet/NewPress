package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/media"
)

// This starts the ordinary serve executable against an actual restored database
// and copied local storage. No HTTP server, media service or worker is injected.
// The small fixture has no plugin jobs or external recipients; its readiness
// result is not acceptance of restored background work or a whole production site.
func TestSnippetApplicationMediaRecovery(t *testing.T) {
	if os.Getenv("NEWPRESS_TEST_SNIPPET_RECOVERY_HTTP") != "1" {
		t.Skip("requires explicit isolated application and media recovery acceptance")
	}
	binary, dsn, pgBin := os.Getenv("NEWPRESS_TEST_BINARY"), os.Getenv("NEWPRESS_TEST_DATABASE_URL"), os.Getenv("NEWPRESS_TEST_PG_BIN")
	u, err := url.Parse(dsn)
	if err != nil || binary == "" || pgBin == "" || u.User == nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("requires an ordinary binary, backup tools and disposable loopback PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	base, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	names := []string{fmt.Sprintf("np_snippet_http_source_%d", time.Now().UnixNano()), fmt.Sprintf("np_snippet_http_clone_%d", time.Now().UnixNano())}
	databaseURL := func(name string) string { v := *u; v.Path = "/" + name; return v.String() }
	var pools []*pgxpool.Pool
	for _, name := range names {
		if _, err = base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
			defer stop()
			if _, e := base.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); e != nil {
				t.Errorf("drop disposable database %s: %v", name, e)
			}
		})
		pool, e := database.Open(ctx, databaseURL(name))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(pool.Close)
		pools = append(pools, pool)
	}
	source, clone := pools[0], pools[1]
	root := t.TempDir()
	reportDir := os.Getenv("NEWPRESS_TEST_SNIPPET_RECOVERY_REPORT")
	if reportDir == "" {
		reportDir = t.TempDir()
	}
	if err = os.MkdirAll(reportDir, 0700); err != nil {
		t.Fatal(err)
	}
	transcript := []map[string]any{}
	record := func(step string, facts map[string]any) {
		facts["step"], facts["at"] = step, time.Now().UTC()
		transcript = append(transcript, facts)
		t.Log("snippet recovery:", step)
	}
	t.Cleanup(func() {
		app, e := os.ReadFile(binary)
		if e != nil {
			t.Error(e)
		}
		raw, e := json.MarshalIndent(map[string]any{"failed": t.Failed(), "parent_pid": os.Getpid(), "application_sha256": fmt.Sprintf("%x", sha256.Sum256(app)), "transcript": transcript,
			"scope": "ordinary serve; local PostgreSQL and one local PNG; no plugin jobs, external services, cloud media or production deployment"}, "", "  ")
		if e != nil {
			t.Error(e)
		}
		f, e := os.OpenFile(filepath.Join(reportDir, "report.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			t.Error(e)
			return
		}
		defer f.Close()
		if _, e = f.Write(append(raw, '\n')); e != nil {
			t.Error(e)
		}
	})
	pgPassword, _ := u.User.Password()
	environment := func(values map[string]string) []string {
		var result []string
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			key = strings.ToUpper(key)
			if !strings.HasPrefix(key, "NEWPRESS_") && !strings.HasPrefix(key, "PG") {
				result = append(result, entry)
			}
		}
		for key, value := range values {
			result = append(result, key+"="+value)
		}
		return result
	}
	cli := func(db string, want int, result any, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir, cmd.Env = root, environment(map[string]string{"NEWPRESS_DATABASE_URL": databaseURL(db), "NEWPRESS_DATA_DIR": filepath.Join(root, "cli-storage"), "NEWPRESS_SEED_DEMO": "false"})
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		e := cmd.Run()
		if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != want {
			t.Fatalf("CLI %v: %v %s", args, e, strings.ReplaceAll(stderr.String(), pgPassword, "[redacted]"))
		}
		if result != nil {
			if e = json.Unmarshal(stdout.Bytes(), result); e != nil {
				t.Fatal("invalid CLI response", e)
			}
		}
	}
	cli(names[0], 0, nil, "migrate", "up")
	users := database.NewUserRepository(source)
	if err = users.EnsureRoles(ctx, auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	const password = "Local snippet recovery password 2026!"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	user, err := users.CreateUser(ctx, auth.UserMutation{Actor: "recovery-fixture"}, auth.UserInput{Username: "recovery-editor", DisplayName: "Recovery editor", RoleSlugs: []string{"administrator"}, Status: auth.UserStatusActive}, hash)
	if err != nil {
		t.Fatal(err)
	}
	a := &clusterHTTPNode{name: "snippet-source", root: filepath.Join(root, "source"), userID: user.ID}
	b := &clusterHTTPNode{name: "snippet-restored", root: filepath.Join(root, "restored"), userID: user.ID}
	defer a.stop(t)
	defer b.stop(t)
	for _, dir := range []string{"data", "themes", "plugins", "updates"} {
		if err = os.MkdirAll(filepath.Join(a.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.CopyFS(filepath.Join(a.root, "themes", "starter"), os.DirFS(filepath.Join("..", "..", "themes", "starter"))); err != nil {
		t.Fatal(err)
	}
	library, err := media.NewManager(filepath.Join(a.root, "data", "media"))
	if err != nil {
		t.Fatal(err)
	}
	picture := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	picture.SetNRGBA(3, 4, color.NRGBA{R: 73, G: 181, B: 216, A: 255})
	var pngBytes bytes.Buffer
	if err = png.Encode(&pngBytes, picture); err != nil {
		t.Fatal(err)
	}
	item, err := library.Upload(ctx, bytes.NewReader(pngBytes.Bytes()), media.UploadInput{Filename: "retained-history.png", Title: "Private history image", UploadedBy: user.Username}, 1<<20)
	if err != nil || item.ChecksumSHA256 != fmt.Sprintf("%x", sha256.Sum256(pngBytes.Bytes())) {
		t.Fatal("upload actual media fixture", err)
	}
	r := database.NewPostRepository(source)
	access := content.SnippetAccess{UserID: user.ID, Identity: fmt.Sprintf("user:%d", user.ID), Actor: user.Username, Read: true, Manage: true}
	privateBody := `<p>private-history-only</p><img src="` + item.PublicURL + `" alt="retained">`
	snippet, err := r.SaveSnippet(ctx, 0, content.Mutation{}, content.SnippetInput{Title: "retained source", ContentSource: privateBody, Visibility: content.SnippetPrivate}, access)
	if err != nil {
		t.Fatal(err)
	}
	snippet, err = r.SaveSnippet(ctx, snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, content.SnippetInput{Title: "shared source", ContentSource: "<p>shared-recovery-body</p>", Visibility: content.SnippetShared}, access)
	if err != nil {
		t.Fatal(err)
	}
	post, err := r.Create(ctx, content.PostInput{Title: "Recovery source", Slug: "recovery-source", Type: content.TypePost, Status: content.StatusPublish, AuthorID: user.ID, Actor: user.Username,
		ContentSource: "<p>ordinary-recovery-body</p>" + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: snippet.CurrentVersion})})
	if err != nil {
		t.Fatal(err)
	}
	start := func(n *clusterHTTPNode, db string) {
		t.Helper()
		n.starts++
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		address := listener.Addr().String()
		_ = listener.Close()
		n.base = "http://" + address
		settings := map[string]string{"NEWPRESS_ADDR": address, "NEWPRESS_PUBLIC_URL": n.base, "NEWPRESS_DATABASE_URL": databaseURL(db), "NEWPRESS_DB_MIN_CONNS": "1", "NEWPRESS_DB_MAX_CONNS": "8", "NEWPRESS_AUTO_MIGRATE": "false", "NEWPRESS_SEED_DEMO": "false", "NEWPRESS_ANALYTICS_ENABLED": "false", "NEWPRESS_ACTIVE_THEME": "starter", "NEWPRESS_UPDATE_CHECK_INTERVAL": "0", "NEWPRESS_PLUGIN_WEBHOOKS_ENABLED": "false"}
		for _, dir := range []string{"data", "themes", "plugins", "updates"} {
			key := "NEWPRESS_" + strings.ToUpper(dir) + "_DIR"
			if dir == "updates" {
				key = "NEWPRESS_UPDATE_DIR"
			}
			settings[key] = filepath.Join(n.root, dir)
		}
		log, e := os.OpenFile(filepath.Join(reportDir, fmt.Sprintf("%s-%d.log", n.name, n.starts)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		n.cmd = exec.CommandContext(ctx, binary, "serve")
		n.cmd.Dir, n.cmd.Env, n.cmd.Stdout, n.cmd.Stderr = n.root, environment(settings), log, log
		if e = n.cmd.Start(); e != nil {
			_ = log.Close()
			t.Fatal(e)
		}
		n.done = make(chan struct{})
		cmd, done := n.cmd, n.done
		go func() { _ = cmd.Wait(); _ = log.Close(); close(done) }()
		jar, _ := cookiejar.New(nil)
		n.client = &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
		clusterEventually(t, 30*time.Second, n.name+" readiness", func() bool {
			select {
			case <-n.done:
				t.Fatal("application exited; see process log", n.name)
			default:
			}
			status, _, e := n.request("GET", "/health/ready", nil)
			return e == nil && status == 200
		})
		form := url.Values{"username": {user.Username}, "password": {password}, "next": {"/admin/"}}
		request, _ := http.NewRequestWithContext(ctx, "POST", n.base+"/admin/login/", strings.NewReader(form.Encode()))
		request.Header.Set("Origin", n.base)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, e := n.client.Do(request)
		if e != nil {
			t.Fatal("login", e)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		status, raw, e := n.request("GET", "/api/v1/admin/session", nil)
		var session struct {
			Authenticated bool   `json:"authenticated"`
			UserID        int64  `json:"user_id"`
			CSRF          string `json:"csrf_token"`
		}
		if e != nil || status != 200 || json.Unmarshal(raw, &session) != nil || !session.Authenticated || session.UserID != user.ID || session.CSRF == "" {
			t.Fatal("restored persistent account login", status, e)
		}
		n.csrf = session.CSRF
		record("application-ready-and-authenticated", map[string]any{"node": n.name, "generation": n.starts, "pid": cmd.Process.Pid, "base": n.base, "database": db})
	}
	request := func(n *clusterHTTPNode, method, path string, value any, headers map[string]string, want int) ([]byte, http.Header) {
		t.Helper()
		var body io.Reader
		if value != nil {
			raw, e := json.Marshal(value)
			if e != nil {
				t.Fatal(e)
			}
			body = bytes.NewReader(raw)
		}
		req, e := http.NewRequestWithContext(ctx, method, n.base+path, body)
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Accept", "application/json")
		if method != "GET" {
			req.Header.Set("Origin", n.base)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-CSRF-Token", n.csrf)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		client := *n.client
		if !strings.HasPrefix(path, "/api/v1/admin/") {
			// Public output and media are checked without the administrator cookie.
			client.Jar = nil
		}
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(method, path, e)
		}
		defer res.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
		if e != nil || len(raw) > 4<<20 || res.StatusCode != want {
			t.Fatalf("%s %s: status %d want %d: %v %.1024s", method, path, res.StatusCode, want, e, raw)
		}
		return raw, res.Header.Clone()
	}
	checkPublic := func(n *clusterHTTPNode, visible bool) {
		t.Helper()
		raw, headers := request(n, "GET", "/api/v1/posts?limit=1", nil, nil, 200)
		if headers.Get("Cache-Control") != "no-store" || bytes.Contains(raw, []byte("shared-recovery-body")) != visible || !bytes.Contains(raw, []byte("ordinary-recovery-body")) || bytes.Contains(raw, []byte("private-history-only")) {
			t.Fatal("public visibility or cache contract changed", string(raw))
		}
	}
	mediaPath := fmt.Sprintf("/api/v1/admin/media/%d", item.ID)
	checkMediaBytes := func(n *clusterHTTPNode) {
		t.Helper()
		raw, _ := request(n, "GET", item.PublicURL, nil, nil, 200)
		if !bytes.Equal(raw, pngBytes.Bytes()) || fmt.Sprintf("%x", sha256.Sum256(raw)) != item.ChecksumSHA256 {
			t.Fatal("restored HTTP media bytes differ")
		}
	}
	checkHistoryReference := func(n *clusterHTTPNode) {
		t.Helper()
		raw, _ := request(n, "GET", mediaPath+"/references", nil, nil, 200)
		var report struct {
			Total     int  `json:"total"`
			InUse     bool `json:"in_use"`
			CanTrash  bool `json:"can_trash"`
			CanDelete bool `json:"can_delete"`
			Items     []struct {
				Source string `json:"source"`
			} `json:"items"`
		}
		if json.Unmarshal(raw, &report) != nil || report.Total != 1 || !report.InUse || report.CanTrash || report.CanDelete || len(report.Items) != 1 || report.Items[0].Source != content.MediaSourceSnippet {
			t.Fatal("private historical media reference not retained", string(raw))
		}
	}
	stable := func(pool *pgxpool.Pool) string {
		t.Helper()
		var facts string
		if e := pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'posts',(SELECT jsonb_agg(to_jsonb(p) - 'snippet_refs_ready' ORDER BY id) FROM np_posts p),
 'revisions',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM np_post_revisions r),
 'history',(SELECT jsonb_agg(to_jsonb(h) ORDER BY snippet_id,version) FROM np_snippet_revisions h),
 'updates',(SELECT jsonb_agg(to_jsonb(u) ORDER BY identity,request_key) FROM np_snippet_receipts u),
 'editorial',(SELECT jsonb_agg(to_jsonb(e) ORDER BY key_hash) FROM np_content_submissions e))::text`).Scan(&facts); e != nil {
			t.Fatal(e)
		}
		return facts
	}
	files := func(dir string) map[string]string {
		t.Helper()
		found := map[string]string{}
		if e := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, e error) error {
			if e != nil || entry.IsDir() {
				return e
			}
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			relative, e := filepath.Rel(dir, path)
			found[filepath.ToSlash(relative)] = fmt.Sprintf("%x", sha256.Sum256(raw))
			return e
		}); e != nil {
			t.Fatal(e)
		}
		return found
	}
	start(a, names[0])
	checkPublic(a, true)
	checkMediaBytes(a)
	checkHistoryReference(a)
	draftURL := fmt.Sprintf("/api/v1/admin/contents/%d/snippet-draft", post.ID)
	draft := content.SnippetDraftRequest{PostID: post.ID, Version: post.Version}
	raw, _ := request(a, "POST", draftURL+"/preview", draft, nil, 200)
	var plan content.SnippetDraftPlan
	if json.Unmarshal(raw, &plan) != nil || plan.Digest == "" || !strings.Contains(plan.ContentHTML, "shared-recovery-body") || strings.Contains(plan.ContentHTML, item.PublicURL) {
		t.Fatal("invalid independent draft preview")
	}
	draft.Digest, draft.RequestKey = plan.Digest, strings.Repeat("d", 64)
	raw, _ = request(a, "POST", draftURL, draft, nil, 201)
	var created struct {
		ID       int64 `json:"id"`
		Version  int64 `json:"version"`
		Replayed bool  `json:"replayed"`
	}
	if json.Unmarshal(raw, &created) != nil || created.ID <= 0 || created.Replayed {
		t.Fatal("independent draft creation failed")
	}
	goodFacts := stable(source)
	record("healthy-source", map[string]any{"source_post_id": post.ID, "draft_id": created.ID, "snippet_id": snippet.ID, "media_id": item.ID, "media_sha256": item.ChecksumSHA256, "only_media_reference": "private snippet history version 1"})
	a.stop(t)
	// Every subsequent CLI mutation and copy runs after the fixture process exits.
	replacement := filepath.Join(root, "verified-repair.json")
	var exported struct{ SHA256 string }
	cli(names[0], 0, &exported, "snippet", "records", "export-repair", "--snippet-id", fmt.Sprint(snippet.ID), "--output", replacement)
	if exported.SHA256 == "" {
		t.Fatal("replacement digest missing")
	}
	if _, err = source.Exec(ctx, `UPDATE np_snippet_revisions SET data=$2 WHERE snippet_id=$1 AND version=1`, snippet.ID, []byte(`{`)); err != nil {
		t.Fatal(err)
	}
	var review database.SnippetMaintenanceReview
	cli(names[0], 1, &review, "snippet", "records", "review", "--snippet-id", fmt.Sprint(snippet.ID))
	var quarantine database.SnippetQuarantineSummary
	cli(names[0], 0, &quarantine, "snippet", "records", "quarantine", "--snippet-id", fmt.Sprint(snippet.ID), "--reference", review.Reference, "--request-key", strings.Repeat("e", 64), "--operator", "fixture", "--reason", "retain damaged private history", "--confirm-all-instances-stopped")
	badFacts := stable(source)
	var originalEvidence []byte
	if err = source.QueryRow(ctx, `SELECT data FROM np_snippet_quarantines WHERE id=$1`, quarantine.ID).Scan(&originalEvidence); err != nil {
		t.Fatal(err)
	}
	sourceFiles := files(a.root)
	mediaFiles := files(filepath.Join(a.root, "data", "media"))
	archive := filepath.Join(root, "unresolved.dump")
	for _, step := range []struct {
		exe, db string
		args    []string
	}{
		{"pg_dump", names[0], []string{"--format=custom", "--no-owner", "--no-acl", "--file", archive}},
		{"pg_restore", names[1], []string{"--no-owner", "--no-acl", "--exit-on-error", "--dbname", names[1], archive}},
	} {
		cmd := exec.CommandContext(ctx, filepath.Join(pgBin, step.exe), step.args...)
		cmd.Dir, cmd.Env = root, environment(map[string]string{"PGHOST": u.Hostname(), "PGPORT": u.Port(), "PGUSER": u.User.Username(), "PGPASSWORD": pgPassword, "PGDATABASE": step.db})
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("database backup/restore: %v %s", e, strings.ReplaceAll(string(out), pgPassword, "[redacted]"))
		}
	}
	if err = os.CopyFS(b.root, os.DirFS(a.root)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files(b.root), sourceFiles) || stable(clone) != badFacts {
		t.Fatal("offline clone differs from source")
	}
	var restoredEvidence []byte
	if err = clone.QueryRow(ctx, `SELECT data FROM np_snippet_quarantines WHERE id=$1`, quarantine.ID).Scan(&restoredEvidence); err != nil || !bytes.Equal(originalEvidence, restoredEvidence) {
		t.Fatal("restored quarantine bytes changed", err)
	}
	record("offline-database-and-local-files-restored", map[string]any{"files": len(sourceFiles), "media_files": mediaFiles, "canonical_facts_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(badFacts))), "quarantine_sha256": fmt.Sprintf("%x", sha256.Sum256(originalEvidence))})
	start(b, names[1])
	checkPublic(b, false)
	checkMediaBytes(b)
	for _, operation := range []struct {
		method, path, code string
		body               any
		headers            map[string]string
	}{
		{"GET", fmt.Sprintf("/api/v1/admin/snippets/%d?view=summary", snippet.ID), "snippets.maintenance", nil, nil},
		{"POST", draftURL, "snippet_draft.maintenance", draft, nil},
		{"POST", "/api/v1/admin/contents", "content.maintenance", map[string]string{"title": "blocked ordinary draft", "content_source": "<p>ordinary</p>"}, map[string]string{"Idempotency-Key": "blocked-recovery-create"}},
		{"GET", mediaPath + "/references", "media.references_unavailable", nil, nil},
		{"POST", mediaPath + "/trash", "media.references_unavailable", nil, map[string]string{"If-Match": fmt.Sprintf(`"media-%d-v%d"`, item.ID, item.Version)}},
	} {
		raw, _ = request(b, operation.method, operation.path, operation.body, operation.headers, 503)
		if !bytes.Contains(raw, []byte(operation.code)) {
			t.Fatal("wrong maintenance error", operation.path, string(raw))
		}
	}
	if stable(clone) != badFacts || !reflect.DeepEqual(files(filepath.Join(b.root, "data", "media")), mediaFiles) {
		t.Fatal("blocked requests changed business facts or media")
	}
	record("unresolved-runtime-gates", map[string]any{"readiness": 200, "guarded_operations": 5, "guarded_status": 503, "public_snippet_hidden": true, "media_bytes_preserved": true})
	b.stop(t)
	var repaired database.SnippetQuarantineSummary
	cli(names[1], 0, &repaired, "snippet", "records", "repair", "--id", quarantine.ID, "--replacement", replacement, "--replacement-sha256", exported.SHA256, "--operator", "fixture", "--reason", "independently checked original fixture", "--confirm-all-instances-stopped", "--confirm-source-verified")
	if repaired.Repair == nil || stable(clone) != goodFacts {
		t.Fatal("repair lost historical content or receipts")
	}
	start(b, names[1])
	checkPublic(b, false)
	checkMediaBytes(b)
	checkHistoryReference(b)
	raw, _ = request(b, "POST", mediaPath+"/trash", nil, map[string]string{"If-Match": fmt.Sprintf(`"media-%d-v%d"`, item.ID, item.Version)}, 409)
	if !bytes.Contains(raw, []byte("media.in_use")) {
		t.Fatal("repaired private history no longer protects media", string(raw))
	}
	retained, err := database.NewPostRepository(clone).GetSnippet(ctx, snippet.ID, access)
	if err != nil || retained.Visibility != content.SnippetWithdrawn || retained.SharedSince != 0 || retained.Version <= snippet.Version || len(retained.Revisions) != 2 {
		t.Fatal("repair revived sharing or lost history", err)
	}
	raw, headers := request(b, "POST", draftURL, draft, nil, 201)
	var replay struct {
		ID       int64 `json:"id"`
		Version  int64 `json:"version"`
		Replayed bool  `json:"replayed"`
	}
	if json.Unmarshal(raw, &replay) != nil || !replay.Replayed || replay.ID != created.ID || replay.Version != created.Version || headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("restored confirmation did not retain the original HTTP result", string(raw))
	}
	raw, _ = request(b, "GET", fmt.Sprintf("/api/v1/admin/contents/%d", created.ID), nil, nil, 200)
	var copied struct {
		Status        string `json:"status"`
		ContentSource string `json:"content_source"`
	}
	if json.Unmarshal(raw, &copied) != nil || copied.Status != content.StatusDraft || copied.ContentSource != plan.ContentHTML {
		t.Fatal("independent draft changed after restore or withdrawal", string(raw))
	}
	if stable(clone) != goodFacts || !reflect.DeepEqual(files(filepath.Join(b.root, "data", "media")), mediaFiles) {
		t.Fatal("replay or rejected trash changed retained facts")
	}
	record("repaired-runtime-recovery", map[string]any{"snippet_visibility": retained.Visibility, "old_confirmation_replayed": true, "draft_id": created.ID, "historical_media_references": 1, "trash_status": 409, "media_sha256": item.ChecksumSHA256})
	b.stop(t)
	var page database.SnippetQuarantinePage
	cli(names[1], 0, &page, "snippet", "records", "quarantines")
	if page.Total != 1 || page.Unresolved != 0 || len(page.Items) != 1 || page.Items[0].Repair == nil ||
		page.Items[0].Repair.ReplacementSHA256 != exported.SHA256 || !page.Items[0].Repair.At.Equal(repaired.Repair.At) {
		t.Fatal("repair audit missing after restart", page)
	}
	if err = clone.QueryRow(ctx, `SELECT data FROM np_snippet_quarantines WHERE id=$1`, quarantine.ID).Scan(&restoredEvidence); err != nil {
		t.Fatal(err)
	}
	var beforeEvidence, afterEvidence map[string]json.RawMessage
	if json.Unmarshal(originalEvidence, &beforeEvidence) != nil || json.Unmarshal(restoredEvidence, &afterEvidence) != nil || len(afterEvidence["repair"]) == 0 {
		t.Fatal("invalid restored evidence envelope")
	}
	delete(afterEvidence, "repair")
	if !reflect.DeepEqual(beforeEvidence, afterEvidence) {
		t.Fatal("repair changed original quarantine fields or raw bytes")
	}
	if stable(source) != badFacts || !reflect.DeepEqual(files(a.root), sourceFiles) {
		t.Fatal("clone maintenance changed the source database or files")
	}
	var sourceEvidence []byte
	if err = source.QueryRow(ctx, `SELECT data FROM np_snippet_quarantines WHERE id=$1`, quarantine.ID).Scan(&sourceEvidence); err != nil || !bytes.Equal(sourceEvidence, originalEvidence) {
		t.Fatal("source quarantine changed", err)
	}
	record("source-preserved-and-repair-audit-retained", map[string]any{"source_files": len(sourceFiles), "quarantines": page.Total, "unresolved": page.Unresolved})
}
