package update

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func zipFixture(t *testing.T, names ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, name := range names {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("fixture content"))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func managerFixture(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	themes := t.TempDir()
	if err := os.WriteFile(filepath.Join(themes, "custom.txt"), []byte("keep custom theme"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := New(root, "0.1.0", themes, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}
func waitPhase(t *testing.T, m *Manager, phases ...string) State {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s, err := m.state()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range phases {
			if s.Phase == p {
				return s
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	s, _ := m.state()
	t.Fatalf("waiting for %v; got %+v", phases, s)
	return State{}
}
func testSource() Source {
	return Source{Owner: "team", Repo: "private", Ref: "release/stable", Manifest: "updates/stable.json", Token: "secret-never-in-output"}
}

func TestCheckDownloadStageAndScopedProgress(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint("corrupt=", corrupt), func(t *testing.T) {
			m := managerFixture(t)
			payload := zipFixture(t, "bin/"+binaryName())
			a := Artifact{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "packages/test.zip", Size: int64(len(payload)), SHA256: digest(payload)}
			if corrupt {
				a.SHA256 = strings.Repeat("0", 64)
			}
			manifest := Manifest{Schema: manifestSchema, Version: "0.2.0", Notes: "A verified release", DataCompatible: true, Artifacts: []Artifact{a}}
			signed := signFixture(t, fixtureKey(t), manifest, 1)
			commit := strings.Repeat("a", 40)
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.URL.Query().Get("access_token") != testSource().Token {
					t.Error("missing private repo token")
				}
				if strings.Contains(r.URL.Path, "/commits/") {
					_ = json.NewEncoder(w).Encode(map[string]string{"sha": commit})
					return
				}
				if r.URL.Query().Get("ref") != commit {
					t.Error("download was not pinned to checked commit")
				}
				if strings.HasSuffix(r.URL.Path, "stable.json") {
					_, _ = w.Write(signed)
					return
				}
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			m.client.base = server.URL
			if err := m.Configure(testSource(), false); err != nil {
				t.Fatal(err)
			}
			if err := m.Check(); err != nil {
				t.Fatal(err)
			}
			waitPhase(t, m, "available")
			view, _ := m.Snapshot()
			revision := view.Revision
			if _, err := m.Start("0.2.0", false, revision); err == nil {
				t.Fatal("backup confirmation bypass")
			}
			ticket, err := m.Start("0.2.0", true, revision)
			if err != nil {
				t.Fatal(err)
			}
			state := waitPhase(t, m, "queued", "failed")
			if corrupt {
				if state.Phase != "failed" {
					t.Fatal(state)
				}
				if _, err := os.Stat(filepath.Join(m.root, "request.json")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("corrupt package queued")
				}
				return
			}
			if state.Phase != "queued" || state.Downloaded != a.Size {
				t.Fatal(state)
			}
			if _, err := m.Start("0.2.0", true, revision); !errors.Is(err, ErrBusy) {
				t.Fatalf("parallel start: %v", err)
			}
			if _, ok := m.Progress(ticket); !ok {
				t.Fatal("ticket did not survive manager state")
			}
			if _, ok := m.Progress(strings.Repeat("f", 64)); ok {
				t.Fatal("invalid ticket accepted")
			}
			second, err := New(m.root, "0.2.0", m.themes, true)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			if _, ok := second.Progress(ticket); !ok {
				t.Fatal("ticket did not survive restart")
			}
			body, err := os.ReadFile(filepath.Join(m.root, "releases", state.ID, "themes", "custom.txt"))
			if err != nil || string(body) != "keep custom theme" {
				t.Fatalf("custom theme not retained: %v", err)
			}
			view, _ = m.Snapshot()
			encoded, _ := json.Marshal(view)
			if strings.Contains(string(encoded), testSource().Token) {
				t.Fatal("token leaked in snapshot")
			}
			if hits.Load() != 7 {
				t.Fatalf("expected three signed checks and one artifact: %d", hits.Load())
			}
		})
	}
}

func TestCancelAndConcurrentOperations(t *testing.T) {
	m := managerFixture(t)
	_ = fixtureKey(t)
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer server.Close()
	m.client.base = server.URL
	if err := m.Configure(testSource(), false); err != nil {
		t.Fatal(err)
	}
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := m.Check(); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err := m.Configure(testSource(), false); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err := m.Cancel(); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, m, "cancelled")
}

func TestNoTokenRedirectOrTransportErrorLeak(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	c := newGitee()
	c.base = source.URL
	_, err := c.get(t.Context(), testSource(), "/raw/manifest.json", "")
	if err == nil || leaked.Load() || strings.Contains(err.Error(), testSource().Token) {
		t.Fatalf("redirect handling: %v", err)
	}
	source.Close()
	_, err = c.get(t.Context(), testSource(), "/raw/manifest.json", "")
	if err == nil || strings.Contains(err.Error(), testSource().Token) || strings.Contains(err.Error(), source.URL) {
		t.Fatalf("transport error leaked details: %v", err)
	}
}

func TestArchiveRejectsTraversalAndRetainsOldTheme(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "themes/../escape", "themes\\evil", "bin/other", "themes/.newpress.lock", "themes/C:/escape", "themes/CON.txt", "themes/file."} {
		t.Run(name, func(t *testing.T) {
			m := managerFixture(t)
			archive := filepath.Join(t.TempDir(), "update.zip")
			if err := os.WriteFile(archive, zipFixture(t, "bin/"+binaryName(), name), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := stageArchive(t.Context(), archive, m.root, randomID(), m.themes); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			data, _ := os.ReadFile(filepath.Join(m.themes, "custom.txt"))
			if string(data) != "keep custom theme" {
				t.Fatal("old themes changed")
			}
		})
	}
	for _, names := range [][]string{{"bin/" + binaryName(), "bin/" + binaryName()}, {"themes/x", "themes/X"}, {"themes/chunnel/theme.json"}} {
		m := managerFixture(t)
		archive := filepath.Join(t.TempDir(), "update.zip")
		_ = os.WriteFile(archive, zipFixture(t, names...), 0600)
		if _, err := stageArchive(t.Context(), archive, m.root, randomID(), m.themes); err == nil {
			t.Fatal("duplicate/missing binary accepted")
		}
	}
}

func TestVersionAndManifestBoundaries(t *testing.T) {
	for _, tc := range []struct {
		candidate, current string
		want               bool
	}{{"0.2.0", "0.1.0", true}, {"0.1.0", "0.2.0", false}, {"0.1.0", "0.1.0", false}, {"1.0.0", "1.0.0-rc.2", true}, {"1.0.0-rc.2", "1.0.0", false}, {"1.0.0-rc.10", "1.0.0-rc.2", true}, {"1.0.0", "development", false}, {"1.0.0", "0.1.0-dev", true}} {
		if got := newer(tc.candidate, tc.current); got != tc.want {
			t.Errorf("newer(%s,%s)=%v", tc.candidate, tc.current, got)
		}
	}
	m := Manifest{Schema: manifestSchema, Version: "0.2.0", DataCompatible: true, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Size: 10, Path: "packages/release.zip", SHA256: strings.Repeat("a", 64)}}}
	if _, err := m.artifact(); err != nil {
		t.Fatal(err)
	}
	m.DataCompatible = false
	if _, err := m.artifact(); err == nil {
		t.Fatal("data migration accepted")
	}
	m.DataCompatible = true
	m.Artifacts[0].OS = "unsupported"
	if _, err := m.artifact(); err == nil {
		t.Fatal("wrong platform accepted")
	}
}

func TestCredentialRetentionClearAndEnvOverride(t *testing.T) {
	m := managerFixture(t)
	s := testSource()
	if err := m.Configure(s, false); err != nil {
		t.Fatal(err)
	}
	s.Token = ""
	if err := m.Configure(s, false); err != nil {
		t.Fatal(err)
	}
	saved, _, _ := loadSource(m.root)
	if saved.Token != testSource().Token {
		t.Fatal("blank token did not retain credential")
	}
	s.Repo = "another"
	if err := m.Configure(s, false); err == nil {
		t.Fatal("old credential followed repository change")
	}
	s.Repo = "private"
	if err := m.Configure(s, true); err != nil {
		t.Fatal(err)
	}
	saved, _, _ = loadSource(m.root)
	if saved.Token != "" {
		t.Fatal("clear token failed")
	}
	t.Setenv("NEWPRESS_GITEE_TOKEN", "environment-secret")
	if err := m.Configure(s, false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(m.root, "source.json"))
	if bytes.Contains(data, []byte("environment-secret")) {
		t.Fatal("environment secret copied to disk")
	}
	snapshot, _ := m.Snapshot()
	if !snapshot.TokenSet || !snapshot.EnvironmentToken || snapshot.Source.Token != "" {
		t.Fatal(snapshot)
	}
}

func TestReadinessRequiresCorrectProcess(t *testing.T) {
	c := &childProcess{instance: "expected", done: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-NewPress-Instance", "other")
		w.WriteHeader(200)
	}))
	defer server.Close()
	if err := awaitReady(context.Background(), c, strings.TrimPrefix(server.URL, "http://"), time.Second); err == nil {
		t.Fatal("accepted a different process at readiness endpoint")
	}
}

func TestUpdateDirectorySeparation(t *testing.T) {
	root := t.TempDir()
	for _, pair := range [][2]string{{"data/updates", "data"}, {"data", "data"}, {"site", "site/data"}} {
		if err := ValidateDirectory(filepath.Join(root, pair[0]), filepath.Join(root, pair[1])); err == nil {
			t.Fatalf("overlapping directories accepted: %v", pair)
		}
	}
	if err := ValidateDirectory(filepath.Join(root, "data-updates"), filepath.Join(root, "data"), filepath.Join(root, "themes")); err != nil {
		t.Fatal(err)
	}
}
