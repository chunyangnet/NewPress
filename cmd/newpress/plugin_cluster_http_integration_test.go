package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
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
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/pluginjobs"
)

type clusterHTTPNode struct {
	name, root, base, csrf string
	userID                 int64
	client                 *http.Client
	cmd                    *exec.Cmd
	done                   chan struct{}
	starts                 int
}

func clusterEventually(t *testing.T, duration time.Duration, label string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatal("cancelled waiting for", label)
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("timed out waiting for", label)
}

func (n *clusterHTTPNode) request(method, path string, value any) (int, []byte, error) {
	var body io.Reader
	if value != nil {
		raw, err := json.Marshal(value)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(method, n.base+path, body)
	if err != nil {
		return 0, nil, err
	}
	r.Header.Set("Accept", "application/json")
	if method != http.MethodGet {
		r.Header.Set("Origin", n.base)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", n.csrf)
	}
	response, err := n.client.Do(r)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	return response.StatusCode, raw, err
}

func (n *clusterHTTPNode) stop(t *testing.T) {
	t.Helper()
	if n.client != nil {
		n.client.CloseIdleConnections()
	}
	if n.cmd == nil || n.cmd.Process == nil {
		return
	}
	select {
	case <-n.done:
	default:
		_ = n.cmd.Process.Kill()
		select {
		case <-n.done:
		case <-time.After(10 * time.Second):
			t.Error("application did not exit", n.name)
		}
	}
}

// This deliberately uses the ordinary compiled application, with no test main,
// alternate transport or injected worker. SQL barriers live only in a freshly
// created disposable database; the signed marketplace is outside this fixture.
func TestPluginTwoHTTPInstancesContentRecovery(t *testing.T) {
	if os.Getenv("NEWPRESS_TEST_PLUGIN_CLUSTER_HTTP") != "1" {
		t.Skip("requires explicit isolated two-HTTP-process acceptance")
	}
	binary, dsn := os.Getenv("NEWPRESS_TEST_BINARY"), os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	baseURL, err := url.Parse(dsn)
	if err != nil || binary == "" || baseURL.User == nil || (baseURL.Hostname() != "127.0.0.1" && baseURL.Hostname() != "localhost") {
		t.Fatal("requires an ordinary application binary and disposable loopback PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	base, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	databaseName := fmt.Sprintf("np_http_cluster_%d", time.Now().UnixNano())
	if _, err = base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		if _, err := base.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{databaseName}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	baseURL.Path = "/" + databaseName
	pool, err := database.Open(ctx, baseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	users := database.NewUserRepository(pool)
	if err = users.EnsureRoles(ctx, auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	const password = "Local cluster acceptance password 2026!"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	user, err := users.CreateUser(ctx, auth.UserMutation{Actor: "isolated-fixture"}, auth.UserInput{Username: "cluster-editor", DisplayName: "Cluster editor", RoleSlugs: []string{"administrator"}, Status: auth.UserStatusActive}, hash)
	if err != nil {
		t.Fatal(err)
	}
	posts := database.NewPostRepository(pool)
	post, err := posts.Create(ctx, content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Slug: "cluster-source", Title: "Original", ContentSource: "<p>Preserved content</p>", AuthorID: user.ID, Actor: "isolated-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	const slug = "cluster-writer"
	wasm, err := os.ReadFile(filepath.Join("..", "..", "internal", "plugin", "testdata", "host-bridge.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := plugin.Manifest{Schema: 1, Slug: slug, Name: "HTTP cluster fixture", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm", Capabilities: []string{"content.write"}, Hooks: map[string][]string{"task.execute.v1": {"np_handle"}}}
	rawManifest, _ := json.Marshal(manifest)
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for name, raw := range map[string][]byte{"plugin.json": rawManifest, "plugin.wasm": wasm} {
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	fixtureRoot := t.TempDir()
	archivePath := filepath.Join(fixtureRoot, "fixture.zip")
	if err = os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	reportDir := os.Getenv("NEWPRESS_TEST_CLUSTER_REPORT")
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
		t.Log("cluster acceptance:", step)
	}
	t.Cleanup(func() {
		appBytes, _ := os.ReadFile(binary)
		data, _ := json.MarshalIndent(map[string]any{"failed": t.Failed(), "database": databaseName, "parent_pid": os.Getpid(), "application_sha256": fmt.Sprintf("%x", sha256.Sum256(appBytes)), "wasm_sha256": fmt.Sprintf("%x", sha256.Sum256(wasm)), "fixture": "ordinary serve executable; test WASM with locally seeded official marker, not marketplace signature acceptance", "transcript": transcript}, "", "  ")
		if err := os.WriteFile(filepath.Join(reportDir, "report.json"), append(data, '\n'), 0600); err != nil {
			t.Error(err)
		}
	})
	controls := database.NewPluginTaskControlRepository(pool)
	nodes := []*clusterHTTPNode{}
	// Join children before test cleanups close pools or drop either database.
	defer func() {
		for _, node := range nodes {
			node.stop(t)
		}
	}()
	for _, name := range []string{"cluster-a", "cluster-b"} {
		n := &clusterHTTPNode{name: name, root: filepath.Join(fixtureRoot, name), userID: user.ID}
		for _, dir := range []string{"data", "themes", "plugins", "updates"} {
			if err = os.MkdirAll(filepath.Join(n.root, dir), 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err = os.CopyFS(filepath.Join(n.root, "themes", "starter"), os.DirFS(filepath.Join("..", "..", "themes", "starter"))); err != nil {
			t.Fatal(err)
		}
		manager := plugin.NewManager(filepath.Join(n.root, "plugins"), plugin.WithTaskControlStore(controls))
		if _, err = manager.InstallPackage(archivePath); err != nil {
			t.Fatal("install local fixture", err)
		}
		// Only package provenance is seeded. Task authorization, configuration,
		// claims, WASM calls and writes all use the ordinary application paths.
		if err = plugin.WritePrivate(filepath.Join(n.root, "plugins", slug, ".official.json"), plugin.Release{Manifest: manifest}); err != nil {
			t.Fatal(err)
		}
		_, configuration, _, err := manager.Configuration(slug)
		if err != nil {
			t.Fatal(err)
		}
		configuration.Grants = []string{"content.write"}
		if err = manager.Configure(slug, configuration); err != nil {
			t.Fatal(err)
		}
		if err = manager.SetEnabled(slug, true); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
	}
	start := func(n *clusterHTTPNode) {
		n.starts++
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		_ = listener.Close()
		n.base = "http://" + address
		u := *baseURL
		q := u.Query()
		q.Set("application_name", n.name)
		u.RawQuery = q.Encode()
		env := []string{}
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "NEWPRESS_") {
				env = append(env, entry)
			}
		}
		settings := map[string]string{"ADDR": address, "PUBLIC_URL": n.base, "DATABASE_URL": u.String(), "DB_MIN_CONNS": "1", "DB_MAX_CONNS": "8", "AUTO_MIGRATE": "false", "SEED_DEMO": "false", "ANALYTICS_ENABLED": "false", "ACTIVE_THEME": "starter", "UPDATE_CHECK_INTERVAL": "0", "PLUGIN_WEBHOOKS_ENABLED": "false"}
		for _, dir := range []string{"data", "themes", "plugins", "updates"} {
			key := strings.ToUpper(dir) + "_DIR"
			if dir == "updates" {
				key = "UPDATE_DIR"
			}
			settings[key] = filepath.Join(n.root, dir)
		}
		for key, value := range settings {
			env = append(env, "NEWPRESS_"+key+"="+value)
		}
		log, err := os.OpenFile(filepath.Join(reportDir, fmt.Sprintf("%s-%d.log", n.name, n.starts)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		n.cmd = exec.CommandContext(ctx, binary, "serve")
		n.cmd.Dir, n.cmd.Env, n.cmd.Stdout, n.cmd.Stderr = n.root, env, log, log
		if err = n.cmd.Start(); err != nil {
			_ = log.Close()
			t.Fatal(err)
		}
		n.done = make(chan struct{})
		cmd, done := n.cmd, n.done
		go func() { _ = cmd.Wait(); _ = log.Close(); close(done) }()
		jar, _ := cookiejar.New(nil)
		n.client = &http.Client{Jar: jar, Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
		clusterEventually(t, 30*time.Second, n.name+" HTTP readiness", func() bool {
			select {
			case <-n.done:
				t.Fatal("application exited during startup; see retained process log", n.name)
			default:
			}
			status, _, err := n.request("GET", "/health/ready", nil)
			return err == nil && status == 200
		})
		record("started", map[string]any{"node": n.name, "pid": cmd.Process.Pid, "root": n.root, "base": n.base, "generation": n.starts})
		form := url.Values{"username": {user.Username}, "password": {password}, "next": {"/admin/"}}
		request, _ := http.NewRequest("POST", n.base+"/admin/login/", strings.NewReader(form.Encode()))
		request.Header.Set("Origin", n.base)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := n.client.Do(request)
		if err != nil {
			t.Fatal("login", err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		status, raw, err := n.request("GET", "/api/v1/admin/session", nil)
		var session struct {
			Authenticated bool   `json:"authenticated"`
			UserID        int64  `json:"user_id"`
			CSRF          string `json:"csrf_token"`
		}
		if err != nil || status != 200 || json.Unmarshal(raw, &session) != nil || !session.Authenticated || session.UserID != user.ID || session.CSRF == "" {
			t.Fatal("persistent account login failed", n.name, status, err)
		}
		n.csrf = session.CSRF
	}
	a, b := nodes[0], nodes[1]
	start(a)
	start(b)
	if a.cmd.Process.Pid == b.cmd.Process.Pid || a.root == b.root || a.base == b.base {
		t.Fatal("HTTP instances collapsed")
	}
	call := func(n *clusterHTTPNode, path string, value any, want int) []byte {
		status, raw, err := n.request("POST", path, value)
		if err != nil || status != want {
			t.Fatalf("%s %s: status %d want %d: %v %s", n.name, path, status, want, err, raw)
		}
		return raw
	}
	configure := func(grants []string) {
		control, found, err := controls.LoadTaskControl(ctx, slug)
		if err != nil || !found {
			t.Fatal("shared configuration missing", err)
		}
		configuration := control.Configuration
		configuration.Grants = grants
		call(a, "/api/v1/admin/plugins/"+slug+"/configuration", map[string]any{"expected_user_id": user.ID, "configuration": configuration}, 200)
	}
	configure([]string{"content.write"})
	jobs := database.NewPluginJobRepository(pool)
	payload := func(version int64, title string) map[string]any {
		return map[string]any{"method": "content.update", "key": "title", "id": post.ID, "value": map[string]any{"version": version, "patch": map[string]any{"title": title}}}
	}
	enqueue := func(n *clusterHTTPNode, key string, value any) pluginjobs.Summary {
		var summary pluginjobs.Summary
		raw := call(n, "/api/v1/admin/plugins/"+slug+"/tasks", map[string]any{"expected_user_id": user.ID, "key": key, "payload": value}, 202)
		if err := json.Unmarshal(raw, &summary); err != nil || summary.ID == "" {
			t.Fatal("invalid task acknowledgement", err)
		}
		return summary
	}
	job := func(id string) pluginjobs.Job {
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var result pluginjobs.Job
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	assertFacts := func(version, receipts int64) {
		got, err := posts.GetByID(ctx, post.ID)
		var actualReceipts, revisions, audits int64
		expectedTitle := map[int64]string{2: "Concurrent once", 3: "Committed before crash", 4: "Restored permission"}[version]
		if err != nil || got.Version != version || got.Title != expectedTitle || got.ContentSource != post.ContentSource {
			t.Fatal("unexpected content state", got.Version, version, err)
		}
		if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_plugin_content_effects),(SELECT count(*) FROM np_post_revisions WHERE post_id=$1),(SELECT count(*) FROM np_audit_events WHERE action='plugin.content.updated')`, post.ID).Scan(&actualReceipts, &revisions, &audits); err != nil || actualReceipts != receipts || audits != receipts || revisions != version {
			t.Fatal("business/receipt/audit facts", actualReceipts, revisions, audits, err)
		}
	}
	// Both HTTP clients send the identical logical request concurrently.
	requests := make(chan struct {
		status int
		body   []byte
		err    error
	}, 2)
	for _, node := range nodes {
		go func(n *clusterHTTPNode) {
			status, raw, err := n.request("POST", "/api/v1/admin/plugins/"+slug+"/tasks", map[string]any{"expected_user_id": user.ID, "key": "concurrent", "payload": payload(1, "Concurrent once")})
			requests <- struct {
				status int
				body   []byte
				err    error
			}{status, raw, err}
		}(node)
	}
	var sameID string
	for range 2 {
		r := <-requests
		var s pluginjobs.Summary
		if r.err != nil || r.status != 202 || json.Unmarshal(r.body, &s) != nil || s.ID == "" || (sameID != "" && s.ID != sameID) {
			t.Fatal("concurrent HTTP admission", r.status, string(r.body), r.err)
		}
		sameID = s.ID
	}
	clusterEventually(t, 15*time.Second, "one shared content effect", func() bool { return job(sameID).Status == "succeeded" })
	assertFacts(2, 1)
	if replay := enqueue(b, "concurrent", payload(1, "Concurrent once")); replay.ID != sameID {
		t.Fatal("HTTP replay changed task identity")
	}
	call(b, "/api/v1/admin/plugins/"+slug+"/tasks", map[string]any{"expected_user_id": user.ID, "key": "concurrent", "payload": payload(1, "Conflicting content")}, 409)
	assertFacts(2, 1)
	record("concurrent HTTP admission and replay", map[string]any{"task_id": sameID, "post_version": 2, "effect_receipts": 1})

	// A PostgreSQL trigger pauses only task completion AFTER the business write
	// has committed. Kill the observed owning application, then let the peer use
	// the real 30-second lease expiry and confirm the original operation receipt.
	barrier, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Release()
	const barrierKey int64 = 1760034901
	if _, err = barrier.Exec(ctx, `SELECT pg_advisory_lock($1)`, barrierKey); err != nil {
		t.Fatal(err)
	}
	defer barrier.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, barrierKey)
	if _, err = pool.Exec(ctx, `CREATE FUNCTION cluster_pause_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='succeeded' AND convert_from(NEW.data,'UTF8')::jsonb->>'key'='crash-after-write' THEN PERFORM pg_advisory_xact_lock(1760034901); END IF; RETURN NEW; END $$; CREATE TRIGGER cluster_pause_completion BEFORE INSERT OR UPDATE ON np_plugin_jobs FOR EACH ROW EXECUTE FUNCTION cluster_pause_completion()`); err != nil {
		t.Fatal(err)
	}
	crash := enqueue(a, "crash-after-write", payload(2, "Committed before crash"))
	var owner string
	clusterEventually(t, 15*time.Second, "committed effect and blocked completion", func() bool {
		var version int64
		_ = pool.QueryRow(ctx, `SELECT version FROM np_posts WHERE id=$1`, post.ID).Scan(&version)
		if version != 3 {
			return false
		}
		_ = pool.QueryRow(ctx, `SELECT application_name FROM pg_stat_activity WHERE datname=current_database() AND application_name IN ('cluster-a','cluster-b') AND wait_event='advisory' LIMIT 1`).Scan(&owner)
		return owner != ""
	})
	assertFacts(3, 2)
	var crashed *clusterHTTPNode
	for _, node := range nodes {
		if node.name == owner {
			crashed = node
		}
	}
	if crashed == nil {
		t.Fatal("cannot identify blocked application")
	}
	oldPID := crashed.cmd.Process.Pid
	crashed.stop(t)
	if _, err = barrier.Exec(ctx, `SELECT pg_advisory_unlock($1)`, barrierKey); err != nil {
		t.Fatal(err)
	}
	clusterEventually(t, 45*time.Second, "peer recovery after actual lease expiry", func() bool { return job(crash.ID).Status == "succeeded" })
	recovered := job(crash.ID)
	if recovered.Attempts != 2 {
		t.Fatal("peer did not recover one expired claim", recovered.Attempts)
	}
	assertFacts(3, 2)
	record("application crash after business commit", map[string]any{"task_id": crash.ID, "killed_node": owner, "killed_pid": oldPID, "attempts": recovered.Attempts, "post_version": 3, "effect_receipts": 2, "lease_duration_seconds": pluginjobs.LeaseDuration.Seconds()})
	start(crashed)
	if crashed.cmd.Process.Pid == oldPID {
		t.Fatal("restart did not replace process")
	}
	assertFacts(3, 2)

	configure([]string{})
	// Keep the already-running peer as the only possible executor so the
	// revocation cannot be observed only by the process that submitted it.
	a.stop(t)
	denied := enqueue(b, "grant-removed", payload(3, "Must not be written"))
	clusterEventually(t, 15*time.Second, "revoked content permission", func() bool {
		state := job(denied.ID).Status
		return state == "retry" || state == "failed" || state == "cancelled"
	})
	deniedJob := job(denied.ID)
	assertFacts(3, 2)
	if deniedJob.Status != "cancelled" {
		call(b, "/api/v1/admin/plugins/"+slug+"/tasks/"+denied.ID, map[string]any{"expected_user_id": user.ID, "version": deniedJob.Version, "action": "cancel"}, 200)
	}
	record("grant revoked through one HTTP instance", map[string]any{"task_id": denied.ID, "state_before_cancel": deniedJob.Status, "error_code": deniedJob.ErrorCode, "effect_receipts": 2, "only_active_executor": b.name})
	start(a)
	configure([]string{"content.write"})
	a.stop(t)
	resumed := enqueue(b, "grant-restored", payload(3, "Restored permission"))
	clusterEventually(t, 15*time.Second, "restored permission on peer", func() bool { return job(resumed.ID).Status == "succeeded" })
	assertFacts(4, 3)
	start(a)
	call(a, "/api/v1/admin/plugins", map[string]any{"expected_user_id": user.ID, "slug": slug, "action": "disable"}, 200)
	call(b, "/api/v1/admin/plugins/"+slug+"/tasks", map[string]any{"expected_user_id": user.ID, "key": "after-disable", "payload": payload(4, "Must stay unchanged")}, 409)
	assertFacts(4, 3)
	page, err := jobs.List(ctx, pluginjobs.Query{Plugin: slug})
	if err != nil || page.Total != 4 {
		t.Fatal("unexpected task count", page.Total, err)
	}
	for _, node := range nodes {
		status, _, err := node.request("GET", "/api/v1/admin/plugins/"+slug+"/tasks", nil)
		if err != nil || status != 200 {
			t.Fatal("peer HTTP task inspection", status, err)
		}
	}
	record("global disable observed by peer", map[string]any{"post_version": 4, "effect_receipts": 3, "tasks": page.Total, "both_http_instances_respond": true})

	// Restore processing must happen while every application is stopped. Seed
	// valid pending/running work through the normal repository, then exercise
	// pg_dump, pg_restore and the actual offline CLI before starting two fresh
	// HTTP directories against the clone. No source-side job is executed here.
	pgBin := os.Getenv("NEWPRESS_TEST_PG_BIN")
	if pgBin == "" {
		t.Fatal("PostgreSQL tools required for clone startup acceptance")
	}
	call(a, "/api/v1/admin/plugins", map[string]any{"expected_user_id": user.ID, "slug": slug, "action": "enable"}, 200)
	for _, node := range nodes {
		node.stop(t)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER cluster_pause_completion ON np_plugin_jobs; DROP FUNCTION cluster_pause_completion()`); err != nil {
		t.Fatal(err)
	}
	control, found, err := controls.LoadTaskControl(ctx, slug)
	if err != nil || !found || !control.Enabled {
		t.Fatal("active source authority before backup", err)
	}
	seedPayload, _ := json.Marshal(payload(4, "Must not execute after restore"))
	in := pluginjobs.Input{Plugin: slug, Epoch: control.Epoch.Token, Kind: "task.execute.v1", ActorID: user.ID, Payload: seedPayload, Key: "clone-running"}
	running, err := jobs.Enqueue(ctx, in, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	claim, claimed, err := jobs.Claim(ctx, time.Now().UTC())
	if err != nil || !claimed || claim.ID != running.ID {
		t.Fatal("source running fixture", claimed, err)
	}
	in.Key = "clone-pending"
	pending, err := jobs.Enqueue(ctx, in, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var originalReceipts string
	if err = pool.QueryRow(ctx, `SELECT string_agg(encode(data,'hex'),'' ORDER BY id) FROM np_plugin_content_effects`).Scan(&originalReceipts); err != nil {
		t.Fatal(err)
	}
	originalPost, err := posts.GetByID(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalRunning, originalPending := job(running.ID), job(pending.ID)
	cloneName := databaseName + "_clone"
	if _, err = base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{cloneName}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		if _, err := base.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{cloneName}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	cleanEnv := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "NEWPRESS_") && !strings.HasPrefix(strings.ToUpper(key), "PG") {
			cleanEnv = append(cleanEnv, value)
		}
	}
	pgPassword, _ := baseURL.User.Password()
	pgEnv := append(append([]string{}, cleanEnv...), "PGHOST="+baseURL.Hostname(), "PGPORT="+baseURL.Port(), "PGUSER="+baseURL.User.Username(), "PGPASSWORD="+pgPassword, "PGDATABASE="+databaseName)
	command := func(name string, env []string, args ...string) []byte {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v %s", filepath.Base(name), err, output)
		}
		return output
	}
	backupPath := filepath.Join(fixtureRoot, "cluster.dump")
	command(filepath.Join(pgBin, "pg_dump"), pgEnv, "--format=custom", "--no-owner", "--no-acl", "--file", backupPath)
	command(filepath.Join(pgBin, "pg_restore"), pgEnv, "--no-owner", "--no-acl", "--exit-on-error", "--dbname", cloneName, backupPath)
	baseURL.Path = "/" + cloneName
	clone, err := database.Open(ctx, baseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clone.Close)
	clonePosts := database.NewPostRepository(clone)
	cliRoot := filepath.Join(fixtureRoot, "clone-maintenance")
	cliEnv := append(append([]string{}, cleanEnv...), "NEWPRESS_DATABASE_URL="+baseURL.String(), "NEWPRESS_DATA_DIR="+filepath.Join(cliRoot, "data"), "NEWPRESS_PLUGINS_DIR="+filepath.Join(cliRoot, "plugins"), "NEWPRESS_THEMES_DIR="+filepath.Join(cliRoot, "themes"), "NEWPRESS_UPDATE_DIR="+filepath.Join(cliRoot, "updates"), "NEWPRESS_AUTO_MIGRATE=false")
	maintenance := command(binary, cliEnv, "plugin", "cancel-restored-tasks", "--confirm-all-workers-stopped")
	if err = os.WriteFile(filepath.Join(reportDir, "restore-cancellation.json"), maintenance, 0600); err != nil {
		t.Fatal(err)
	}
	cloneControls := database.NewPluginTaskControlRepository(clone)
	retired, found, err := cloneControls.LoadTaskControl(ctx, slug)
	if err != nil || !found || retired.Enabled || !retired.Retired {
		t.Fatal("clone authority not retired", err)
	}
	for _, id := range []string{running.ID, pending.ID} {
		var state string
		if err = clone.QueryRow(ctx, `SELECT status FROM np_plugin_jobs WHERE id=$1`, id).Scan(&state); err != nil || state != "cancelled" {
			t.Fatal("restored task was not cancelled", state, err)
		}
	}
	for _, node := range nodes {
		oldRoot := node.root
		node.root = filepath.Join(fixtureRoot, "clone-"+node.name)
		for _, dir := range []string{"data", "updates"} {
			if err = os.MkdirAll(filepath.Join(node.root, dir), 0700); err != nil {
				t.Fatal(err)
			}
		}
		for _, dir := range []string{"themes", "plugins"} {
			if err = os.CopyFS(filepath.Join(node.root, dir), os.DirFS(filepath.Join(oldRoot, dir))); err != nil {
				t.Fatal(err)
			}
		}
		start(node)
	}
	// Observe several ordinary worker ticks through both live HTTP servers.
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		for _, node := range nodes {
			status, raw, err := node.request("GET", "/api/v1/admin/plugins/"+slug+"/tasks", nil)
			var got pluginjobs.Page
			if err != nil || status != 200 || json.Unmarshal(raw, &got) != nil || got.Total != 6 {
				t.Fatal("restored HTTP task listing", status, err)
			}
			seenRestored := 0
			for _, item := range got.Items {
				if item.ID == pending.ID || item.ID == running.ID {
					seenRestored++
					if item.Status != "cancelled" {
						t.Fatal("clone worker resumed restored task")
					}
				}
			}
			if seenRestored != 2 {
				t.Fatal("restored tasks missing from HTTP listing")
			}
		}
		var receipts string
		got, err := clonePosts.GetByID(ctx, post.ID)
		if err != nil || !reflect.DeepEqual(got, originalPost) {
			t.Fatal("clone changed restored content", err)
		}
		if err = clone.QueryRow(ctx, `SELECT string_agg(encode(data,'hex'),'' ORDER BY id) FROM np_plugin_content_effects`).Scan(&receipts); err != nil || receipts != originalReceipts {
			t.Fatal("clone changed receipt bytes", err)
		}
		var revisions, audits int64
		if err = clone.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_post_revisions WHERE post_id=$1),(SELECT count(*) FROM np_audit_events WHERE action='plugin.content.updated')`, post.ID).Scan(&revisions, &audits); err != nil || revisions != 4 || audits != 3 {
			t.Fatal("clone changed revision or content audit counts", revisions, audits, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	assertFacts(4, 3)
	if !reflect.DeepEqual(job(running.ID), originalRunning) || !reflect.DeepEqual(job(pending.ID), originalPending) {
		t.Fatal("clone recovery changed the stopped source database")
	}
	finalSource, err := posts.GetByID(ctx, post.ID)
	if err != nil || !reflect.DeepEqual(finalSource, originalPost) {
		t.Fatal("clone recovery changed the source content", err)
	}
	var finalSourceReceipts string
	if err = pool.QueryRow(ctx, `SELECT string_agg(encode(data,'hex'),'' ORDER BY id) FROM np_plugin_content_effects`).Scan(&finalSourceReceipts); err != nil || finalSourceReceipts != originalReceipts {
		t.Fatal("clone recovery changed the source receipt bytes", err)
	}
	record("real dump restore and two HTTP clone startups", map[string]any{"clone_database": cloneName, "pending_id": pending.ID, "running_id": running.ID, "restored_states": "cancelled", "authority_retired": true, "post_version": 4, "full_post_preserved": true, "revisions": 4, "content_update_audits": 3, "original_receipt_bytes_preserved": true, "receipt_hex_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(originalReceipts))), "source_tasks_unchanged": true, "observation_seconds": 3})
}
