package database

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/testwasm"
)

type authorityProcessRequest struct {
	Sequence      int                    `json:"sequence"`
	Operation     string                 `json:"operation"`
	Configuration plugin.Configuration   `json:"configuration,omitempty"`
	ActorID       int64                  `json:"actor_id,omitempty"`
	Job           pluginjobs.Job         `json:"job,omitempty"`
	Effect        *plugineffects.Request `json:"effect,omitempty"`
}

type authorityProcessReply struct {
	Sequence      int                       `json:"sequence"`
	Operation     string                    `json:"operation"`
	Phase         string                    `json:"phase"`
	PID           int                       `json:"pid"`
	ParentPID     int                       `json:"parent_pid"`
	Root          string                    `json:"root"`
	Database      string                    `json:"database"`
	Schema        string                    `json:"schema"`
	At            time.Time                 `json:"at"`
	DatabaseAt    time.Time                 `json:"database_at"`
	Error         string                    `json:"error,omitempty"`
	ErrorKind     string                    `json:"error_kind,omitempty"`
	Control       *plugin.TaskControl       `json:"control,omitempty"`
	Configuration plugin.Configuration      `json:"configuration"`
	Epoch         plugin.TaskEpoch          `json:"epoch"`
	State         plugin.ExecutionStatus    `json:"state"`
	Enabled       bool                      `json:"enabled"`
	Reads         []int64                   `json:"reads,omitempty"`
	Checks        int                       `json:"checks,omitempty"`
	Worked        bool                      `json:"worked,omitempty"`
	Subscriptions []pluginjobs.Subscription `json:"subscriptions,omitempty"`
	Effect        *plugineffects.Result     `json:"effect,omitempty"`
}

type authorityCloseAfterClaimExecutor struct {
	*plugin.TaskExecutor
	closeAuthority func()
}

func (e *authorityCloseAfterClaimExecutor) Claim(ctx context.Context, queue pluginjobs.Store, now time.Time) (pluginjobs.Job, bool, error) {
	claim, ok, err := e.TaskExecutor.Claim(ctx, queue, now)
	if err == nil && ok {
		e.closeAuthority()
	}
	return claim, ok, err
}

func authorityErrorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, pluginjobs.ErrAuthorization), errors.Is(err, plugin.ErrInactive):
		return "authorization"
	case errors.Is(err, plugin.ErrTaskControlUnavailable), errors.Is(err, plugineffects.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, plugin.ErrConfigurationConflict), errors.Is(err, plugin.ErrTaskControlConflict), errors.Is(err, plugineffects.ErrConflict), errors.Is(err, content.ErrVersionConflict):
		return "conflict"
	default:
		return "other"
	}
}

// Only a child launched by the independent-process test enters this fixture.
// os.Exit keeps the stdout pipe a pure JSON protocol, without testing's PASS.
func TestPostgreSQLPluginTaskAuthorityProcess(t *testing.T) {
	if os.Getenv("NEWPRESS_TASK_AUTHORITY_PROCESS") != "1" {
		t.Skip("private independent-process fixture is not requested")
	}
	if err := runTaskAuthorityProcess(t.Context()); err != nil {
		fmt.Fprintln(os.Stderr, "independent authority fixture failed:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runTaskAuthorityProcess(ctx context.Context) error {
	configuration, err := pgxpool.ParseConfig(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if err != nil {
		return err
	}
	configuration.ConnConfig.RuntimeParams["search_path"] = os.Getenv("NEWPRESS_TASK_AUTHORITY_SCHEMA")
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		return err
	}
	defer pool.Close()
	authorityPool, err := pgxpool.NewWithConfig(ctx, configuration.Copy())
	if err != nil {
		return err
	}
	defer authorityPool.Close()
	controls := NewPluginTaskControlRepository(authorityPool)
	if err := controls.Ready(ctx); err != nil {
		return err
	}
	root := os.Getenv("NEWPRESS_TASK_AUTHORITY_ROOT")
	m := plugin.NewManager(root, plugin.WithTaskControlStore(controls))
	if err := m.Recover(); err != nil {
		return err
	}
	manifest, err := m.InstallPackage(os.Getenv("NEWPRESS_TASK_AUTHORITY_PACKAGE"))
	if err != nil {
		// A restarted fixture reuses its already verified local installation.
		if _, statErr := os.Stat(filepath.Join(root, "content-writer", "plugin.json")); statErr != nil {
			return err
		}
		manifest, _, _, err = m.Configuration("content-writer")
		if err != nil {
			return err
		}
	}
	if err := plugin.WritePrivate(filepath.Join(root, manifest.Slug, ".official.json"), plugin.Release{Manifest: manifest}); err != nil {
		return err
	}
	jobs, users := NewPluginJobRepository(pool), NewUserRepository(pool)
	input, output := json.NewDecoder(bufio.NewReader(os.Stdin)), json.NewEncoder(os.Stdout)
	var database, schema string
	if err := pool.QueryRow(ctx, `SELECT current_database(),current_schema()`).Scan(&database, &schema); err != nil {
		return err
	}
	reply := func(request authorityProcessRequest, phase string) authorityProcessReply {
		r := authorityProcessReply{Sequence: request.Sequence, Operation: request.Operation, Phase: phase,
			PID: os.Getpid(), ParentPID: os.Getppid(), Root: root, Database: database, Schema: schema, At: time.Now().UTC()}
		_ = pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&r.DatabaseAt)
		return r
	}
	if err := output.Encode(reply(authorityProcessRequest{Operation: "ready"}, "ready")); err != nil {
		return err
	}
	for {
		var request authorityProcessRequest
		if err := input.Decode(&request); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		r := reply(request, "complete")
		var operationError error
		switch request.Operation {
		case "configuration":
			_, r.Configuration, r.State, operationError = m.Configuration(manifest.Slug)
		case "configure":
			if request.ActorID > 0 {
				operationError = m.ConfigureAs(manifest.Slug, request.Configuration, request.ActorID)
			} else {
				operationError = m.Configure(manifest.Slug, request.Configuration)
			}
		case "enable":
			operationError = m.SetEnabled(manifest.Slug, true)
		case "disable":
			operationError = m.SetEnabled(manifest.Slug, false)
		case "epoch":
			r.Epoch, operationError = m.CurrentTaskEpoch(manifest.Slug)
		case "list":
			var items []plugin.Installed
			items, operationError = m.List()
			if len(items) == 1 {
				r.Enabled = items[0].Enabled
			}
		case "subscriptions":
			r.Subscriptions, operationError = m.TaskSubscriptions(ctx)
		case "uninstall":
			operationError = m.Uninstall(manifest.Slug)
		case "close-authority-pool":
			authorityPool.Close()
		case "content-direct":
			if request.Effect == nil {
				return errors.New("missing content request")
			}
			var result plugineffects.Result
			result, operationError = NewPluginContentEffectRepository(pool).WriteContent(ctx, *request.Effect)
			if operationError == nil {
				r.Effect = &result
			}
		case "execute", "execute-paused", "tick", "tick-close-authority-after-claim", "content-execute", "content-paused", "content-lost-response", "content-crash-after-commit":
			executor := &plugin.TaskExecutor{Manager: m}
			if strings.HasPrefix(request.Operation, "content-") {
				executor.Effects = processContentEffectFunc(func(call context.Context, effect plugineffects.Request) (plugineffects.Result, error) {
					if request.Operation == "content-paused" {
						paused := r
						paused.Phase, paused.At = "write-started", time.Now().UTC()
						if err := output.Encode(paused); err != nil {
							return plugineffects.Result{}, err
						}
						var resume authorityProcessRequest
						if err := input.Decode(&resume); err != nil {
							return plugineffects.Result{}, err
						}
						if resume.Sequence != request.Sequence || resume.Operation != "resume" {
							return plugineffects.Result{}, errors.New("incorrect content continuation")
						}
					}
					result, err := NewPluginContentEffectRepository(pool).WriteContent(call, effect)
					if err != nil {
						return result, err
					}
					if request.Operation == "content-lost-response" {
						return plugineffects.Result{}, plugineffects.ErrUnavailable
					}
					r.Effect = &result
					if request.Operation == "content-crash-after-commit" {
						committed := r
						committed.Phase, committed.At = "write-committed", time.Now().UTC()
						if err := output.Encode(committed); err != nil {
							return plugineffects.Result{}, err
						}
						os.Exit(23)
					}
					return result, nil
				})
			}
			executor.Authorize = func(call context.Context, job pluginjobs.Job) error {
				r.Checks++
				if err := jobs.CheckSharedClaim(call, job, time.Now().UTC()); err != nil {
					return err
				}
				user, err := users.GetUser(call, job.ActorID)
				if errors.Is(err, auth.ErrUserNotFound) {
					return pluginjobs.ErrAuthorization
				}
				if err != nil {
					return err
				}
				if user.ID <= 0 || user.Status != auth.UserStatusActive || !user.Can(auth.CapabilityAdminAccess) || !user.Can(auth.CapabilityPluginsManage) {
					return pluginjobs.ErrAuthorization
				}
				return nil
			}
			executor.Read = func(call context.Context, id int64) (any, error) {
				r.Reads = append(r.Reads, id)
				if request.Operation == "execute-paused" && len(r.Reads) == 1 {
					paused := r
					paused.Phase, paused.At = "read-started", time.Now().UTC()
					if err := output.Encode(paused); err != nil {
						return nil, err
					}
					var resume authorityProcessRequest
					if err := input.Decode(&resume); err != nil {
						return nil, err
					}
					if resume.Sequence != request.Sequence || resume.Operation != "resume" {
						return nil, errors.New("incorrect execution continuation")
					}
				}
				if err := call.Err(); err != nil {
					return nil, err
				}
				return map[string]any{"id": id, "public": true}, nil
			}
			if request.Operation == "tick" || request.Operation == "tick-close-authority-after-claim" {
				worker := pluginjobs.Worker{Queue: jobs, Executor: executor}
				if request.Operation == "tick-close-authority-after-claim" {
					worker.Executor = &authorityCloseAfterClaimExecutor{TaskExecutor: executor, closeAuthority: authorityPool.Close}
				}
				r.Worked, operationError = worker.Tick(ctx)
			} else {
				operationError = executor.Execute(ctx, request.Job)
			}
			_, _, r.State, _ = m.Configuration(manifest.Slug)
		case "quit":
			if err := output.Encode(r); err != nil {
				return err
			}
			return nil
		default:
			return errors.New("unknown authority process operation")
		}
		if control, found, err := NewPluginTaskControlRepository(pool).LoadTaskControl(ctx, manifest.Slug); err == nil && found {
			r.Control = &control
		}
		if operationError != nil {
			r.Error, r.ErrorKind = operationError.Error(), authorityErrorKind(operationError)
		}
		r.At = time.Now().UTC()
		_ = pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&r.DatabaseAt)
		if err := output.Encode(r); err != nil {
			return err
		}
	}
}

type authorityChild struct {
	command   *exec.Cmd
	stdin     io.WriteCloser
	encoder   *json.Encoder
	responses chan authorityProcessReply
	readError chan error
	exited    chan error
	sequence  int
	pid       int
	root      string
	closed    bool
	stderr    *bytes.Buffer
}

func startAuthorityChild(t *testing.T, schema, root, archive string) *authorityChild {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPostgreSQLPluginTaskAuthorityProcess$")
	command.Env = append(os.Environ(), "NEWPRESS_TASK_AUTHORITY_PROCESS=1", "NEWPRESS_TASK_AUTHORITY_SCHEMA="+schema,
		"NEWPRESS_TASK_AUTHORITY_ROOT="+root, "NEWPRESS_TASK_AUTHORITY_PACKAGE="+archive)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	child := &authorityChild{command: command, stdin: stdin, encoder: json.NewEncoder(stdin), responses: make(chan authorityProcessReply, 8), readError: make(chan error, 1), exited: make(chan error, 1), root: root, stderr: &bytes.Buffer{}}
	command.Stderr = child.stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	child.pid = command.Process.Pid
	go func() {
		decoder := json.NewDecoder(stdout)
		for {
			var r authorityProcessReply
			if err := decoder.Decode(&r); err != nil {
				child.readError <- err
				child.exited <- command.Wait()
				return
			}
			child.responses <- r
		}
	}()
	t.Cleanup(func() {
		if child.closed {
			return
		}
		_ = child.stdin.Close()
		select {
		case <-child.exited:
		case <-time.After(3 * time.Second):
			_ = child.command.Process.Kill()
			<-child.exited
		}
	})
	return child
}

func (c *authorityChild) send(t *testing.T, request authorityProcessRequest) int {
	t.Helper()
	c.sequence++
	request.Sequence = c.sequence
	if err := c.encoder.Encode(request); err != nil {
		t.Fatal(err)
	}
	return c.sequence
}

func (c *authorityChild) receive(t *testing.T, sequence int, phase string) authorityProcessReply {
	t.Helper()
	select {
	case r := <-c.responses:
		if r.PID != c.pid || r.ParentPID != os.Getpid() || r.Root != c.root || r.Sequence != sequence || r.Phase != phase || r.At.IsZero() || r.DatabaseAt.IsZero() {
			t.Fatal("process identity/protocol mismatch", r)
		}
		return r
	case err := <-c.readError:
		select {
		case r := <-c.responses:
			if r.PID == c.pid && r.ParentPID == os.Getpid() && r.Root == c.root && r.Sequence == sequence && r.Phase == phase && !r.At.IsZero() && !r.DatabaseAt.IsZero() {
				return r
			}
		default:
		}
		t.Fatal("authority process protocol failed", err)
	case <-time.After(8 * time.Second):
		t.Fatal("authority process response deadline exceeded")
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	return authorityProcessReply{}
}

func authorityProcessArchive(t *testing.T) (string, plugin.Manifest, string) {
	t.Helper()
	code := testwasm.HostSequence([]byte(`{"method":"content.get","id":1}`), []byte(`{"method":"content.get","id":2}`))
	manifest := plugin.Manifest{Schema: 1, Slug: "independent-reader", Name: "Independent reader", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm",
		Capabilities: []string{"settings.read", "settings.write", "content.read", "cache"},
		Hooks:        map[string][]string{"task.execute.v1": {"np_handle"}, "event.deliver.v1": {"np_handle"}, "admin.action": {"np_handle"}},
		Settings:     []plugin.Setting{{Key: "count", Label: "Count", Type: "number", Default: float64(0), Max: 10}}}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	raw, _ := json.Marshal(manifest)
	for _, item := range []struct {
		name string
		data []byte
	}{{"plugin.json", raw}, {"plugin.wasm", code}} {
		file, err := writer.Create(item.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(item.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "independent-reader.zip")
	if err := os.WriteFile(path, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(code)
	return path, manifest, hex.EncodeToString(digest[:])
}

func TestPostgreSQLPluginTaskAuthorityIndependentProcesses(t *testing.T) {
	ctx, pool, jobs := newPluginJobsIntegrationStore(t)
	controls, users := NewPluginTaskControlRepository(pool), NewUserRepository(pool)
	if err := users.EnsureRoles(ctx, auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	password, err := auth.HashPassword("Independent authority fixture password")
	if err != nil {
		t.Fatal(err)
	}
	createAdmin := func(name string) auth.User {
		user, err := users.CreateUser(ctx, auth.UserMutation{Actor: "authority-fixture"}, auth.UserInput{Username: name, DisplayName: name, Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"administrator"}}, password)
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	actor, otherActor := createAdmin("authorityadmin"), createAdmin("authorityadminother")
	archive, manifest, digest := authorityProcessArchive(t)
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]
	transcript := []map[string]any{}
	identities, exits := []authorityProcessReply{}, []map[string]any{}
	t.Cleanup(func() {
		if path := os.Getenv("NEWPRESS_TASK_AUTHORITY_REPORT"); path != "" {
			result := "passed"
			if t.Failed() {
				result = "failed"
			}
			report := map[string]any{"schema": 1, "result": result, "controller_pid": os.Getpid(), "module_sha256": digest, "processes": identities, "exits": exits, "transcript": transcript}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			encoder := json.NewEncoder(file)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(report); err != nil {
				t.Error(err)
			}
		}
	})
	a := startAuthorityChild(t, schema, filepath.Join(t.TempDir(), "process-a"), archive)
	b := startAuthorityChild(t, schema, filepath.Join(t.TempDir(), "process-b"), archive)
	record := func(step string, r authorityProcessReply) authorityProcessReply {
		if r.Schema != schema || r.Database != pool.Config().ConnConfig.Database {
			t.Fatal("independent process used a different database/schema", r)
		}
		transcript = append(transcript, map[string]any{"step": step, "received_at": time.Now().UTC(), "reply": r})
		t.Logf("authority pid=%d sequence=%d operation=%s phase=%s result=%s reads=%v", r.PID, r.Sequence, r.Operation, r.Phase, r.ErrorKind, r.Reads)
		return r
	}
	call := func(child *authorityChild, step string, request authorityProcessRequest) authorityProcessReply {
		seq := child.send(t, request)
		return record(step, child.receive(t, seq, "complete"))
	}
	success := func(r authorityProcessReply) authorityProcessReply {
		if r.ErrorKind != "" {
			t.Fatal("independent process operation failed", r.Operation, r.Error)
		}
		return r
	}
	for _, child := range []*authorityChild{a, b} {
		ready := record("ready", child.receive(t, 0, "ready"))
		identities = append(identities, ready)
	}
	if a.pid == b.pid || a.pid == os.Getpid() || b.pid == os.Getpid() || a.root == b.root {
		t.Fatal("independent process/directory fixture collapsed")
	}
	enqueueCurrent := func(key string) pluginjobs.Job {
		control := loadPGTaskControl(t, ctx, controls, manifest.Slug)
		job, err := jobs.Enqueue(ctx, pluginjobs.Input{Plugin: manifest.Slug, Epoch: control.Epoch.Token,
			Kind: "task.execute.v1", Key: key, ActorID: actor.ID, Payload: json.RawMessage(`{}`)}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	expectUnchanged := func(job pluginjobs.Job) {
		stored, err := scanTask(pool.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1`, job.ID))
		if err != nil || !reflect.DeepEqual(stored, job) {
			t.Fatal("unavailable local runtime changed a valid task", stored.Status, stored.Version, stored.Attempts, err)
		}
	}
	expectSucceeded := func(job pluginjobs.Job) {
		stored, err := scanTask(pool.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1`, job.ID))
		if err != nil || stored.Status != "succeeded" || stored.Attempts != 1 {
			t.Fatal("matching process did not complete the retained task", stored.Status, stored.Attempts, err)
		}
	}
	initialA := success(call(a, "initial-a", authorityProcessRequest{Operation: "configuration"}))
	initialB := success(call(b, "initial-b", authorityProcessRequest{Operation: "configuration"}))
	if initialA.Configuration.Version != 0 || initialB.Configuration.Version != 0 {
		t.Fatal("startup promoted a local configuration")
	}
	if r := call(a, "enable-before-review", authorityProcessRequest{Operation: "enable"}); r.ErrorKind == "" || r.Control != nil {
		t.Fatal("startup enabled without shared review")
	}
	c := initialA.Configuration
	c.Grants = manifest.Capabilities
	success(call(a, "first-shared-save", authorityProcessRequest{Operation: "configure", Configuration: c}))
	success(call(a, "enable-a", authorityProcessRequest{Operation: "enable"}))
	locallyDisabled := enqueueCurrent("local-package-still-disabled")
	if r := success(call(b, "disabled-local-worker-leaves-valid-task", authorityProcessRequest{Operation: "tick"})); r.Worked || len(r.Reads) != 0 {
		t.Fatal("locally disabled instance claimed active shared work", r.Worked, r.Reads)
	}
	expectUnchanged(locallyDisabled)
	if r := success(call(a, "enabled-local-worker-completes-task", authorityProcessRequest{Operation: "tick"})); !r.Worked || !reflect.DeepEqual(r.Reads, []int64{1, 2}) {
		t.Fatal("matching enabled instance did not execute", r.Worked, r.Reads)
	}
	expectSucceeded(locallyDisabled)
	success(call(b, "enable-b", authorityProcessRequest{Operation: "enable"}))
	sharedA := success(call(a, "shared-a", authorityProcessRequest{Operation: "configuration"}))
	sharedB := success(call(b, "shared-b", authorityProcessRequest{Operation: "configuration"}))
	if sharedA.Control == nil || sharedB.Control == nil || sharedA.Control.Epoch != sharedB.Control.Epoch {
		t.Fatal("different directories did not share the same authority")
	}
	// Both processes submit the same old configuration version without waiting
	// for one another's response. Exactly one explicit administrator save wins.
	first, second := sharedA.Configuration, sharedB.Configuration
	first.Values["count"], second.Values["count"] = float64(1), float64(2)
	seqA := a.send(t, authorityProcessRequest{Operation: "configure", Configuration: first})
	seqB := b.send(t, authorityProcessRequest{Operation: "configure", Configuration: second})
	resultA := record("configuration-race-a", a.receive(t, seqA, "complete"))
	resultB := record("configuration-race-b", b.receive(t, seqB, "complete"))
	if !((resultA.ErrorKind == "" && resultB.ErrorKind == "conflict") || (resultB.ErrorKind == "" && resultA.ErrorKind == "conflict")) {
		t.Fatal("independent configuration CAS winners", resultA.ErrorKind, resultB.ErrorKind)
	}
	current := loadPGTaskControl(t, ctx, controls, manifest.Slug)
	if current.Configuration.Version != sharedA.Configuration.Version+1 || current.Epoch.Token == sharedA.Control.Epoch.Token {
		t.Fatal("winning administrator save did not rotate approval")
	}
	claim := func(key, kind string, actorID int64) pluginjobs.Job {
		control := loadPGTaskControl(t, ctx, controls, manifest.Slug)
		if _, err := jobs.Enqueue(ctx, pluginjobs.Input{Plugin: manifest.Slug, Epoch: control.Epoch.Token, Kind: kind, Key: key, ActorID: actorID, Payload: json.RawMessage(`{}`)}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		job, ok, err := jobs.Claim(ctx, time.Now().UTC())
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		return job
	}
	finish := func(job pluginjobs.Job, code string) {
		if err := jobs.Finish(ctx, job, code, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	execute := func(child *authorityChild, step string, job pluginjobs.Job, want string, reads []int64) authorityProcessReply {
		r := call(child, step, authorityProcessRequest{Operation: "execute", Job: job})
		if r.ErrorKind != want || !reflect.DeepEqual(r.Reads, reads) {
			t.Fatal("independent execution mismatch", r.ErrorKind, r.Reads, r.Error)
		}
		return r
	}
	job := claim("fresh-task", "task.execute.v1", actor.ID)
	if r := execute(b, "fresh-task", job, "", []int64{1, 2}); r.Checks < 4 {
		t.Fatal("claim/account was not rechecked for each host call")
	}
	finish(job, "")
	job = claim("old-configuration-task", "task.execute.v1", actor.ID)
	latest := success(call(a, "before-rotation", authorityProcessRequest{Operation: "configuration"}))
	success(call(a, "rotate-configuration", authorityProcessRequest{Operation: "configure", Configuration: latest.Configuration}))
	execute(b, "reject-old-configuration", job, "authorization", nil)
	finish(job, "authorization_changed")
	pause := func(job pluginjobs.Job) int {
		seq := b.send(t, authorityProcessRequest{Operation: "execute-paused", Job: job})
		r := record("first-host-read-started", b.receive(t, seq, "read-started"))
		if !reflect.DeepEqual(r.Reads, []int64{1}) {
			t.Fatal("pause did not occur inside the first read", r.Reads)
		}
		return seq
	}
	resume := func(seq int, step string) {
		if err := b.encoder.Encode(authorityProcessRequest{Sequence: seq, Operation: "resume"}); err != nil {
			t.Fatal(err)
		}
		r := record(step, b.receive(t, seq, "complete"))
		if r.ErrorKind != "authorization" || !reflect.DeepEqual(r.Reads, []int64{1}) || r.State.Failures != 0 || r.State.Consecutive != 0 {
			t.Fatal("old worker read after withdrawal or cooled its plugin", r.ErrorKind, r.Reads, r.State)
		}
	}
	job = claim("remote-disable", "task.execute.v1", actor.ID)
	seq := pause(job)
	success(call(a, "remote-disable-commit", authorityProcessRequest{Operation: "disable"}))
	resume(seq, "remote-disable-observed")
	finish(job, "authorization_changed")
	if r := success(call(b, "remote-disable-list", authorityProcessRequest{Operation: "list"})); r.Enabled {
		t.Fatal("list retained local enabled after shared disable")
	}
	success(call(a, "reenable-after-disable", authorityProcessRequest{Operation: "enable"}))
	job = claim("account-revoked", "task.execute.v1", actor.ID)
	seq = pause(job)
	actor, err = users.UpdateUser(ctx, actor.ID, auth.UserMutation{Actor: "authority-fixture", ExpectedVersion: actor.Version}, auth.UserInput{Username: actor.Username, DisplayName: actor.DisplayName, Status: auth.UserStatusSuspended, Locale: "zh-CN", RoleSlugs: []string{"administrator"}})
	if err != nil {
		t.Fatal(err)
	}
	transcript = append(transcript, map[string]any{"step": "persistent-account-suspended", "at": time.Now().UTC(), "actor_id": actor.ID, "version": actor.Version})
	resume(seq, "account-revocation-observed")
	finish(job, "authorization_changed")
	actor, err = users.UpdateUser(ctx, actor.ID, auth.UserMutation{Actor: "authority-fixture", ExpectedVersion: actor.Version}, auth.UserInput{Username: actor.Username, DisplayName: actor.DisplayName, Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"administrator"}})
	if err != nil {
		t.Fatal(err)
	}
	latest = success(call(a, "before-subscription", authorityProcessRequest{Operation: "configuration"}))
	success(call(a, "explicit-subscription", authorityProcessRequest{Operation: "configure", Configuration: latest.Configuration, ActorID: actor.ID}))
	job = claim("subscribed-event", "event.deliver.v1", actor.ID)
	execute(a, "subscribed-event", job, "", []int64{1, 2})
	finish(job, "")
	job = claim("different-subscription-actor", "event.deliver.v1", otherActor.ID)
	execute(b, "reject-different-event-actor", job, "authorization", nil)
	finish(job, "authorization_changed")
	if r := success(call(a, "enable-clears-subscription", authorityProcessRequest{Operation: "enable"})); r.Control == nil || r.Control.Epoch.ActorID != 0 {
		t.Fatal("enable silently retained subscription consent")
	}
	job = claim("event-without-consent", "event.deliver.v1", actor.ID)
	execute(b, "reject-event-without-consent", job, "authorization", nil)
	finish(job, "authorization_changed")
	success(call(b, "uninstall-from-other-process", authorityProcessRequest{Operation: "uninstall"}))
	if r := call(a, "uninstall-observed", authorityProcessRequest{Operation: "epoch"}); r.ErrorKind != "authorization" || r.Control == nil || !r.Control.Retired || r.Control.Configuration.Version != 0 {
		t.Fatal("uninstall did not retire shared authority", r.ErrorKind)
	}
	if r := call(a, "old-installation-cannot-reenable", authorityProcessRequest{Operation: "enable"}); r.ErrorKind == "" {
		t.Fatal("old installation reused retired approval")
	}
	latest = success(call(a, "retired-configuration-preview", authorityProcessRequest{Operation: "configuration"}))
	latest.Configuration.Grants = manifest.Capabilities
	success(call(a, "explicit-review-after-retirement", authorityProcessRequest{Operation: "configure", Configuration: latest.Configuration}))
	success(call(a, "enable-fresh-authority", authorityProcessRequest{Operation: "enable"}))
	latest = success(call(a, "fresh-subscription-preview", authorityProcessRequest{Operation: "configuration"}))
	success(call(a, "fresh-explicit-subscription", authorityProcessRequest{Operation: "configure", Configuration: latest.Configuration, ActorID: actor.ID}))
	if r := success(call(b, "global-subscription-without-local-package", authorityProcessRequest{Operation: "subscriptions"})); len(r.Subscriptions) != 1 || r.Subscriptions[0].ActorID != actor.ID {
		t.Fatal("shared discovery depended on a local package", r.Subscriptions)
	}
	locallyAbsent := enqueueCurrent("other-instance-has-no-package")
	if r := success(call(b, "missing-local-worker-leaves-valid-task", authorityProcessRequest{Operation: "tick"})); r.Worked || len(r.Reads) != 0 {
		t.Fatal("missing local package claimed active shared work", r.Worked, r.Reads)
	}
	expectUnchanged(locallyAbsent)
	if r := success(call(a, "matching-worker-completes-remote-task", authorityProcessRequest{Operation: "tick"})); !r.Worked || !reflect.DeepEqual(r.Reads, []int64{1, 2}) {
		t.Fatal("matching process did not execute retained remote work", r.Worked, r.Reads)
	}
	expectSucceeded(locallyAbsent)
	// Close the real authorization pool immediately after scoped acquisition.
	// The separate queue pool must still persist retry; this is not a full outage.
	control := loadPGTaskControl(t, ctx, controls, manifest.Slug)
	pending, err := jobs.Enqueue(ctx, pluginjobs.Input{Plugin: manifest.Slug, Epoch: control.Epoch.Token, Kind: "task.execute.v1", Key: "unavailable-authority", ActorID: actor.ID, Payload: json.RawMessage(`{}`)}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if r := success(call(a, "worker-persists-retry-after-claim-outage", authorityProcessRequest{Operation: "tick-close-authority-after-claim"})); !r.Worked || len(r.Reads) != 0 {
		t.Fatal("unavailable authority did not stop the guest")
	}
	retried, err := scanTask(pool.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1`, pending.ID))
	if err != nil || retried.Status != "retry" || retried.ErrorCode != "execution_failed" {
		t.Fatal("unavailable authority cancelled instead of retrying", retried.Status, retried.ErrorCode, err)
	}
	transcript = append(transcript, map[string]any{"step": "unavailable-authority-task-retained", "job_id": retried.ID, "status": retried.Status, "error_code": retried.ErrorCode})
	beforeAcquisition := enqueueCurrent("authority-unavailable-before-claim")
	if r := call(a, "unavailable-authority-blocks-acquisition", authorityProcessRequest{Operation: "tick"}); r.ErrorKind != "unavailable" || r.Worked || len(r.Reads) != 0 {
		t.Fatal("authority outage before acquisition changed work", r.ErrorKind, r.Worked, r.Reads)
	}
	expectUnchanged(beforeAcquisition)
	if r := call(a, "unavailable-authority-is-not-disabled-list", authorityProcessRequest{Operation: "list"}); r.ErrorKind != "unavailable" {
		t.Fatal("list concealed a real shared availability failure", r.ErrorKind)
	}
	for _, child := range []*authorityChild{a, b} {
		success(call(child, "quit", authorityProcessRequest{Operation: "quit"}))
		_ = child.stdin.Close()
		select {
		case err := <-child.exited:
			child.closed = true
			if err != nil {
				t.Fatal("authority child failed on exit", err, child.stderr.String())
			}
			exits = append(exits, map[string]any{"pid": child.pid, "exit_code": 0, "at": time.Now().UTC()})
		case <-time.After(5 * time.Second):
			t.Fatal("authority child did not exit")
		}
	}
}
