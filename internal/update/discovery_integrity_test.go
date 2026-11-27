package update

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/dirlock"
)

func TestDiscoveryRejectsPartiallyDecodedCache(t *testing.T) {
	t.Setenv("NEWPRESS_UPDATE_CHECK_INTERVAL", "1h")
	t.Setenv("NEWPRESS_UPDATE_OBSERVE_FOR", "2s")
	checked := strconv.Quote(time.Now().UTC().Format(time.RFC3339Nano))
	for _, test := range []struct {
		name, body string
	}{
		{"unknown field first", `{"unknown":true,"checked":` + checked + `,"message":"untrusted result"}`},
		{"unknown field last", `{"checked":` + checked + `,"message":"untrusted result","unknown":true}`},
		{"message type error", `{"checked":` + checked + `,"message":42}`},
		{"interval type error", `{"checked":` + checked + `,"message":"untrusted result","interval":42}`},
		{"invalid checked time", `{"message":"untrusted result","checked":"invalid timestamp"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := managerFixture(t)
			filename := filepath.Join(manager.root, "discovery.json")
			if err := os.WriteFile(filename, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := manager.discovery(); got != (Discovery{}) {
				t.Errorf("invalid cache returned partial fields: %+v", got)
			}
			view, err := manager.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if !view.Discovery.Checked.IsZero() || view.Discovery.Message != "" || view.Discovery.Interval != "1h0m0s" {
				t.Errorf("snapshot exposed invalid discovery fields: %+v", view.Discovery)
			}
			if !manager.checkDue() {
				t.Error("invalid cache postponed the next automatic check")
			}
			if got, err := os.ReadFile(filename); err != nil || string(got) != test.body {
				t.Errorf("reading discovery changed the original cache: %q, %v", got, err)
			}
		})
	}
}

func TestAutomaticCheckDueRecoversFromClockRollback(t *testing.T) {
	t.Setenv("NEWPRESS_UPDATE_OBSERVE_FOR", "2s")
	now := time.Now().UTC()
	for _, test := range []struct {
		name     string
		checked  time.Time
		missing  bool
		disabled bool
		want     bool
	}{
		{name: "missing cache", missing: true, want: true},
		{name: "zero time", want: true},
		{name: "recent check", checked: now.Add(-5 * time.Minute)},
		{name: "overdue check", checked: now.Add(-2 * time.Hour), want: true},
		{name: "clock moved back", checked: now.Add(2 * time.Hour), want: true},
		{name: "disabled with future time", checked: now.Add(2 * time.Hour), disabled: true},
		{name: "disabled without cache", missing: true, disabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			interval := "1h"
			if test.disabled {
				interval = "0"
			}
			t.Setenv("NEWPRESS_UPDATE_CHECK_INTERVAL", interval)
			manager := managerFixture(t)
			if !test.missing {
				if err := writeJSON(filepath.Join(manager.root, "discovery.json"), Discovery{Checked: test.checked, Message: "previous check"}); err != nil {
					t.Fatal(err)
				}
			}
			if got := manager.checkDue(); got != test.want {
				t.Errorf("checkDue() = %v, want %v", got, test.want)
			}
		})
	}
}

func discoveryProbe(t *testing.T) (*Manager, *atomic.Int32) {
	t.Helper()
	t.Setenv("NEWPRESS_UPDATE_CHECK_INTERVAL", "1h")
	t.Setenv("NEWPRESS_UPDATE_OBSERVE_FOR", "2s")
	manager := managerFixture(t)
	key := fixtureKey(t)
	manifest := Manifest{Version: "0.2.0", DataCompatible: true, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "pkg.zip", Size: 10, SHA256: strings.Repeat("a", 64)}}}
	signed := signFixture(t, key, manifest, 1)
	commit := strings.Repeat("b", 40)
	hits := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Get("access_token") != testSource().Token {
			t.Error("scheduled check did not use the configured source")
		}
		switch {
		case strings.Contains(r.URL.Path, "/commits/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": commit})
		case strings.HasSuffix(r.URL.Path, "stable.json") && r.URL.Query().Get("ref") == commit:
			_, _ = w.Write(signed)
		default:
			t.Error("scheduled discovery attempted an unexpected download", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	manager.client.base = server.URL
	if err := manager.Configure(testSource(), false); err != nil {
		t.Fatal(err)
	}
	return manager, hits
}

func TestScheduledDiscoveryRecoversUnusableCache(t *testing.T) {
	for _, kind := range []string{"unknown field", "wrong field type", "future time", "overdue", "missing"} {
		t.Run(kind, func(t *testing.T) {
			manager, hits := discoveryProbe(t)
			when := time.Now().UTC()
			if kind == "future time" {
				when = when.Add(24 * time.Hour)
			} else if kind == "overdue" {
				when = when.Add(-2 * time.Hour)
			}
			body := `{"checked":` + strconv.Quote(when.Format(time.RFC3339Nano)) + `,"message":"previous check"}`
			if kind == "unknown field" {
				body = strings.TrimSuffix(body, "}") + `,"unknown":true}`
			} else if kind == "wrong field type" {
				body = strings.TrimSuffix(body, "}") + `,"interval":false}`
			}
			filename := filepath.Join(manager.root, "discovery.json")
			if kind != "missing" {
				if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			statePath := filepath.Join(manager.root, "state.json")
			state := State{ID: "previous-installation", Phase: "rolled_back", Percent: 100, Target: "0.1.1", Previous: "0.1.0", Logs: []Log{{Time: when.Add(-time.Hour), Level: "warning", Message: "Keep the installation receipt"}}}
			if err := manager.save(state); err != nil {
				t.Fatal(err)
			}
			originalState, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now().UTC()
			manager.scheduledCheck(t.Context())
			if got := hits.Load(); got != 2 {
				t.Errorf("automatic discovery made %d requests, want commit and signed manifest requests", got)
			}
			view, err := manager.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if view.Available == nil || view.Available.Version != "0.2.0" || view.Revision == "" {
				t.Errorf("automatic discovery did not publish a verified candidate: %+v", view.Available)
			}
			if view.Discovery.Checked.Before(started) || view.Discovery.Checked.After(time.Now()) || view.Discovery.Message != "可用版本 0.2.0" {
				t.Errorf("automatic discovery did not replace the unusable cache: %+v", view.Discovery)
			}
			if manager.checkDue() {
				t.Error("successful recovery did not restore the configured interval")
			}
			manager.scheduledCheck(t.Context())
			if got := hits.Load(); got != 2 {
				t.Errorf("check repeated before the next interval: requests=%d", got)
			}
			if after, err := os.ReadFile(statePath); err != nil || !bytes.Equal(after, originalState) {
				t.Errorf("automatic discovery changed the installation receipt: %v", err)
			}
		})
	}
}

func TestScheduledDiscoveryRecoveryHonorsExclusions(t *testing.T) {
	for _, kind := range []string{"disabled", "queued job", "rollback protection", "operation lock"} {
		t.Run(kind, func(t *testing.T) {
			manager, hits := discoveryProbe(t)
			switch kind {
			case "disabled":
				t.Setenv("NEWPRESS_UPDATE_CHECK_INTERVAL", "0")
			case "queued job":
				if err := manager.save(State{ID: "active-job", Phase: "queued"}); err != nil {
					t.Fatal(err)
				}
			case "rollback protection":
				if err := manager.save(State{ID: "protected-job", Phase: "completed", ProtectedUntil: time.Now().Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
			case "operation lock":
				lock, err := dirlock.Acquire(filepath.Join(manager.root, "operation"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = lock.Close() })
			}
			body := `{"checked":` + strconv.Quote(time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)) + `,"message":"invalid cache","unknown":true}`
			filename := filepath.Join(manager.root, "discovery.json")
			if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := manager.state()
			if err != nil {
				t.Fatal(err)
			}
			manager.scheduledCheck(t.Context())
			if hits.Load() != 0 {
				t.Error("automatic recovery bypassed disabled checks, an active job, or an operation lock")
			}
			if after, err := os.ReadFile(filename); err != nil || string(after) != body {
				t.Errorf("suppressed check changed discovery cache: %v", err)
			}
			after, err := manager.state()
			if err != nil || after.ID != before.ID || after.Phase != before.Phase || !after.ProtectedUntil.Equal(before.ProtectedUntil) {
				t.Errorf("suppressed check changed task state: %+v, %v", after, err)
			}
			if _, err := os.Stat(filepath.Join(manager.root, "available.json")); !os.IsNotExist(err) {
				t.Errorf("suppressed check published an available candidate: %v", err)
			}
		})
	}
}
