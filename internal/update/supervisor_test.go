package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/config"
	appversion "newpress.local/newpress/internal/version"
)

// The test binary doubles as a real serving subprocess. This exercises pipe
// shutdown, process identity, immutable binaries, commit and rollback on Windows
// and Unix without depending on a shell or Docker.
func TestMain(m *testing.M) {
	// OTA fixtures use 0.2/0.3 releases independently of the shipping version.
	appversion.Version = "0.1.0-dev"
	if os.Getenv("NEWPRESS_TEST_UPDATE_CHILD") == "1" {
		fixtureProcess()
		return
	}
	os.Exit(m.Run())
}
func fixtureProcess() {
	exe, _ := os.Executable()
	version := "0.1.0-dev"
	_ = readJSON(filepath.Join(filepath.Dir(exe), "fixture-version.json"), &version)
	if len(os.Args) > 1 && os.Args[1] == "update-info" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"protocol": Protocol, "version": version, "launcher_generation": LauncherGeneration})
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "backup" {
		if err := os.WriteFile(os.Args[3], []byte("fixture backup"), 0600); err != nil {
			os.Exit(2)
		}
		return
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "fail-start")); err == nil {
		os.Exit(9)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "hang-start")); err == nil {
		for {
			time.Sleep(time.Second)
		}
	}
	// Let the test request a crash only after it observes the relevant durable phase.
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		go func() {
			marker := filepath.Join(filepath.Dir(exe), "exit-now")
			for {
				if _, err := os.Stat(marker); err == nil {
					os.Exit(9)
				}
				time.Sleep(25 * time.Millisecond)
			}
		}()
	}
	server := &http.Server{Addr: os.Getenv("NEWPRESS_ADDR"), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-NewPress-Instance", os.Getenv("NEWPRESS_UPDATE_INSTANCE"))
		fmt.Fprint(w, version)
	})}
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); _ = server.Close() }()
	_ = server.ListenAndServe()
}

func copyTestRelease(t *testing.T, root, version string, fail bool) Release {
	t.Helper()
	r := Release{ID: randomID(), Version: version}
	directory := filepath.Dir(executable(root, r, ""))
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(executable(root, r, ""), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	out.Close()
	if err = writeJSON(filepath.Join(directory, "fixture-version.json"), version); err != nil {
		t.Fatal(err)
	}
	if fail {
		if err = os.WriteFile(filepath.Join(directory, "fail-start"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	r.BinaryHash, err = hashFile(executable(root, r, ""))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSupervisorCutoverRollbackAndRestart(t *testing.T) {
	t.Setenv("NEWPRESS_UPDATE_OBSERVE_FOR", "2m")
	t.Setenv("NEWPRESS_TEST_UPDATE_CHILD", "1")
	root := t.TempDir()
	t.Setenv("NEWPRESS_UPDATE_DIR", root)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("NEWPRESS_ADDR", address)
	cfg := config.Config{Address: address, DataDir: t.TempDir(), ThemesDir: t.TempDir(), PluginsDir: t.TempDir()}
	m, err := New(root, "0.1.0-dev", cfg.ThemesDir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Error("supervisor did not stop")
		}
	})
	waitHTTPVersion := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			response, e := (&http.Client{Timeout: time.Second}).Get("http://" + address + "/health/ready")
			if e == nil {
				b, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if string(b) == want {
					return
				}
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("expected ready version %s", want)
	}
	waitHTTPVersion("0.1.0-dev")
	policy, err := m.policy()
	if err != nil {
		t.Fatal(err)
	}
	policy.ObservationSeconds = 2
	if _, err = m.ConfigurePolicy(policy.Revision, &policy.Policy); err != nil {
		t.Fatal(err)
	}
	// The supervisor waits for three readiness observations before consuming jobs.
	good := copyTestRelease(t, root, "0.2.0", false)
	queue := func(r Release) {
		s := State{ID: r.ID, Target: r.Version, StartedAt: time.Now().UTC()}
		s.step("queued", 82, "fixture ready")
		if err = m.save(s); err != nil {
			t.Fatal(err)
		}
		if err = writeJSON(filepath.Join(root, "request.json"), Transaction{Generation: LauncherGeneration, Expires: time.Now().Add(time.Hour), Candidate: r, State: s}); err != nil {
			t.Fatal(err)
		}
	}
	queue(good)
	completed := waitPhase(t, m, "completed")
	if !completed.BlocksChanges() || completed.ProtectedUntil.Sub(completed.UpdatedAt) != 2*time.Second || completed.Previous != "0.1.0-dev" {
		t.Fatalf("missing post-commit protection: %+v", completed)
	}
	if err := m.Check(); !errors.Is(err, ErrBusy) {
		t.Fatalf("check replaced protected receipt: %v", err)
	}
	waitHTTPVersion("0.2.0")
	var active Release
	if err = readJSON(filepath.Join(root, "active.json"), &active); err != nil || active.ID != good.ID {
		t.Fatalf("active not committed: %+v %v", active, err)
	}
	if _, err = os.Stat(filepath.Join(root, "backup-"+good.ID+".zip")); err != nil {
		t.Fatal("offline backup missing")
	}
	bad := copyTestRelease(t, root, "0.3.0", true)
	queue(bad)
	waitPhase(t, m, "rolled_back")
	waitHTTPVersion("0.2.0")
	if err = readJSON(filepath.Join(root, "active.json"), &active); err != nil || active.ID != good.ID {
		t.Fatal("rollback did not restore active release")
	}
	if _, err = os.Stat(filepath.Join(root, "transaction.json")); !os.IsNotExist(err) {
		t.Fatal("finished journal not removed")
	}
	late := copyTestRelease(t, root, "0.4.0", false)
	queue(late)
	waitPhase(t, m, "observing")
	if err = os.WriteFile(filepath.Join(filepath.Dir(executable(root, late, "")), "exit-now"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, m, "rolled_back")
	waitHTTPVersion("0.2.0")
	if readJSON(filepath.Join(root, "active.json"), &active) != nil || active.ID != good.ID {
		t.Fatal("crash in probation changed committed release")
	}
	committed := copyTestRelease(t, root, "0.5.0", false)
	queue(committed)
	waitPhase(t, m, "completed")
	if err = os.WriteFile(filepath.Join(filepath.Dir(executable(root, committed, "")), "exit-now"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, m, "rolled_back")
	waitHTTPVersion("0.2.0")
	if _, err = os.Stat(filepath.Join(root, "guard.json")); !os.IsNotExist(err) {
		t.Fatal("one-shot recovery guard survived rollback")
	}
}

func TestSupervisorRecoversInterruptedTransaction(t *testing.T) {
	t.Setenv("NEWPRESS_TEST_UPDATE_CHILD", "1")
	t.Setenv("NEWPRESS_UPDATE_OBSERVE_FOR", "invalid")
	root := t.TempDir()
	t.Setenv("NEWPRESS_UPDATE_DIR", root)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("NEWPRESS_ADDR", address)
	cfg := config.Config{Address: address, DataDir: t.TempDir(), ThemesDir: t.TempDir(), PluginsDir: t.TempDir()}
	m, err := New(root, "0.1.0-dev", cfg.ThemesDir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	tx := Transaction{Candidate: Release{ID: randomID(), Version: "0.3.0"}, Previous: Release{Version: "0.1.0-dev"}, State: State{ID: randomID(), Phase: "starting"}}
	// Even a damaged site policy and invalid defaults cannot block journal recovery.
	if err = writeJSON(filepath.Join(root, "policy.json"), Policy{}); err != nil {
		t.Fatal(err)
	}
	if err = writeJSON(filepath.Join(root, "transaction.json"), tx); err != nil {
		t.Fatal(err)
	}
	if err = writeJSON(filepath.Join(root, "active.json"), tx.Candidate); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	waitPhase(t, m, "rolled_back")
	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("recovery did not stop")
	}
	var active Release
	if readJSON(filepath.Join(root, "active.json"), &active) != nil || active.ID != "" {
		t.Fatal("incomplete release survived recovery")
	}
}

func TestSupervisorRejectsInvalidDeploymentPolicyOnCleanBoot(t *testing.T) {
	t.Setenv("NEWPRESS_TEST_UPDATE_CHILD", "1")
	t.Setenv("NEWPRESS_UPDATE_DIR", t.TempDir())
	t.Setenv("NEWPRESS_UPDATE_CHECK_INTERVAL", "")
	t.Setenv("NEWPRESS_UPDATE_OBSERVE_FOR", "")
	cfg := config.Config{Address: "127.0.0.1:0", DataDir: t.TempDir(), ThemesDir: t.TempDir(), PluginsDir: t.TempDir()}
	for _, key := range []string{"NEWPRESS_UPDATE_CHECK_INTERVAL", "NEWPRESS_UPDATE_OBSERVE_FOR"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "invalid")
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := Run(ctx, cfg); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("invalid deployment accepted: %v", err)
			}
		})
	}
}

func TestFailedCandidateCleanupIsBounded(t *testing.T) {
	t.Setenv("NEWPRESS_TEST_UPDATE_CHILD", "1")
	root := t.TempDir()
	r := copyTestRelease(t, root, "0.2.0", false)
	if err := os.WriteFile(filepath.Join(filepath.Dir(executable(root, r, "")), "hang-start"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := startChild(root, os.Args[0], config.Config{ThemesDir: t.TempDir()}, r)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	c.abort(100 * time.Millisecond)
	if time.Since(started) > 5*time.Second {
		t.Fatal("hung candidate prevented rollback")
	}
}
