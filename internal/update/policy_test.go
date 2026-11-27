package update

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeploymentPolicyRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("NEWPRESS_UPDATE_OBSERVE_FOR", "2m")
	for _, raw := range []string{"wrong", "-1m", "59s", "169h"} {
		t.Setenv("NEWPRESS_UPDATE_CHECK_INTERVAL", raw)
		if _, err := deploymentPolicy(); err == nil || !strings.Contains(err.Error(), "NEWPRESS_UPDATE_CHECK_INTERVAL") {
			t.Fatalf("interval %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"0", "0s", "0m"} {
		t.Setenv("NEWPRESS_UPDATE_CHECK_INTERVAL", raw)
		p, err := deploymentPolicy()
		if err != nil || p.AutoCheck || p.CheckIntervalMinutes != 360 {
			t.Fatalf("disabled interval %q: %+v %v", raw, p, err)
		}
	}
	t.Setenv("NEWPRESS_UPDATE_CHECK_INTERVAL", "1m")
	m := managerFixture(t)
	base, err := m.policy()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := m.ConfigurePolicy(base.Revision, &base.Policy)
	if err != nil {
		t.Fatal(err)
	}
	// A custom policy stays usable when a later deployment has bad defaults.
	for _, key := range []string{"NEWPRESS_UPDATE_CHECK_INTERVAL", "NEWPRESS_UPDATE_OBSERVE_FOR"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "invalid")
			if _, err := m.ConfigurePolicy(saved.Revision, nil); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("reset to bad defaults: %v", err)
			}
			current, err := m.policy()
			if err != nil || current != saved {
				t.Fatal("reset lost the working policy", current, err)
			}
		})
	}
}

func TestPolicyOverridesPersistAndResetToDeployment(t *testing.T) {
	t.Setenv("NEWPRESS_UPDATE_CHECK_INTERVAL", "0")
	t.Setenv("NEWPRESS_UPDATE_OBSERVE_FOR", "3m")
	t.Setenv("NEWPRESS_UPDATE_POLICY_PROTOCOL", "")
	m := managerFixture(t)
	defaults, err := m.Snapshot()
	if err != nil || defaults.Policy.Customized || defaults.Policy.AutoCheck || defaults.Policy.CheckIntervalMinutes != 360 || defaults.Policy.ObservationSeconds != 180 || defaults.Policy.LauncherAppliesWindow || defaults.Discovery.Interval != "0s" {
		t.Fatal(defaults, err)
	}
	// Saving identical values still pins the site to them, independent of future deployment changes.
	pinned, err := m.ConfigurePolicy(defaults.Policy.Revision, &defaults.Policy.Policy)
	if err != nil || pinned.Revision == defaults.Policy.Revision || !pinned.Customized {
		t.Fatal(pinned, err)
	}
	other, err := New(m.root, m.current, m.themes, true)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	t.Setenv("NEWPRESS_UPDATE_POLICY_PROTOCOL", policyProtocol)
	t.Setenv("NEWPRESS_UPDATE_CHECK_INTERVAL", "90s")
	t.Setenv("NEWPRESS_UPDATE_OBSERVE_FOR", "2500ms")
	seen, err := other.Snapshot()
	if err != nil || seen.Policy.Revision != pinned.Revision || seen.Policy.Policy != pinned.Policy || !seen.Policy.LauncherAppliesWindow {
		t.Fatal(seen, err)
	}
	next := Policy{AutoCheck: true, CheckIntervalMinutes: 1.25, ObservationSeconds: 3.5}
	saved, err := other.ConfigurePolicy(pinned.Revision, &next)
	if err != nil || saved.interval() != 75*time.Second || saved.window() != 3500*time.Millisecond {
		t.Fatal(saved, err)
	}
	if _, err = m.ConfigurePolicy(pinned.Revision, nil); !errors.Is(err, ErrPolicyConflict) {
		t.Fatalf("stale reset: %v", err)
	}
	if !m.checkDue() {
		t.Fatal("re-enabling automatic checks did not make the first check due")
	}
	if err = writeJSON(filepath.Join(m.root, "discovery.json"), Discovery{Checked: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if m.checkDue() {
		t.Fatal("an already checked source became due too soon")
	}
	restored, err := m.ConfigurePolicy(saved.Revision, nil)
	if err != nil || restored.Customized || !restored.AutoCheck || restored.CheckIntervalMinutes != 1.5 || restored.ObservationSeconds != 2.5 {
		t.Fatal(restored, err)
	}
	if _, err = os.Stat(filepath.Join(m.root, "policy.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reset kept the override", err)
	}
}

func TestPolicyValidationAndActiveJobExclusion(t *testing.T) {
	t.Setenv("NEWPRESS_UPDATE_CHECK_INTERVAL", "")
	t.Setenv("NEWPRESS_UPDATE_OBSERVE_FOR", "")
	m := managerFixture(t)
	base, err := m.policy()
	if err != nil || !base.AutoCheck || base.interval() != 6*time.Hour || base.window() != 2*time.Minute {
		t.Fatal(base, err)
	}
	for _, p := range []Policy{{false, 0, 2}, {true, 10081, 2}, {false, 1, 1.9}, {true, 1, 601}, {true, math.NaN(), 2}} {
		if _, err = m.ConfigurePolicy(base.Revision, &p); err == nil {
			t.Fatal("accepted out-of-range policy", p)
		}
	}
	for _, p := range []Policy{{true, 1, 2}, {false, 10080, 600}} {
		base, err = m.ConfigurePolicy(base.Revision, &p)
		if err != nil || base.Policy != p {
			t.Fatal(base, err)
		}
	}
	for _, state := range []State{{Phase: "queued"}, {Phase: "completed", ProtectedUntil: time.Now().Add(time.Minute)}} {
		if err = m.save(state); err != nil {
			t.Fatal(err)
		}
		if _, err = m.ConfigurePolicy(base.Revision, nil); !errors.Is(err, ErrBusy) {
			t.Fatalf("%s allowed a policy change: %v", state.Phase, err)
		}
	}
}

func TestPolicyConcurrentManagersDoNotOverwrite(t *testing.T) {
	m := managerFixture(t)
	other, err := New(m.root, m.current, m.themes, true)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	base, err := m.policy()
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, manager := range []*Manager{m, other} {
		go func() {
			<-start
			_, err := manager.ConfigurePolicy(base.Revision, &Policy{true, 60, float64(10 + i)})
			results <- err
		}()
	}
	close(start)
	success := 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if !errors.Is(err, ErrBusy) && !errors.Is(err, ErrPolicyConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent saves succeeded %d times", success)
	}
	current, err := m.policy()
	if err != nil || !current.Customized || current.Revision == base.Revision {
		t.Fatal(current, err)
	}
}
