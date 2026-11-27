package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/pluginjobs"
)

// This test store models revision CAS and ambiguous commit responses. The SQL
// implementation, capacity and independent processes are tested with real PG.
type testTaskControls struct {
	mu          sync.Mutex
	rows        map[string]TaskControl
	loadError   error
	intercept   func()
	failVersion string
	failBlocked bool
	failAfter   bool
}

func cloneTaskControl(c TaskControl) TaskControl {
	b, _ := json.Marshal(c)
	var out TaskControl
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *testTaskControls) LoadTaskControl(ctx context.Context, slug string) (TaskControl, bool, error) {
	if err := ctx.Err(); err != nil {
		return TaskControl{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadError != nil {
		return TaskControl{}, false, s.loadError
	}
	c, ok := s.rows[slug]
	return cloneTaskControl(c), ok, nil
}

func (s *testTaskControls) ListTaskControls(ctx context.Context) ([]TaskControl, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadError != nil {
		return nil, s.loadError
	}
	controls := make([]TaskControl, 0, len(s.rows))
	for _, control := range s.rows {
		controls = append(controls, cloneTaskControl(control))
	}
	return controls, nil
}

func (s *testTaskControls) SaveTaskControl(ctx context.Context, expected int64, c TaskControl) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateTaskControl(c); err != nil {
		return err
	}
	s.mu.Lock()
	intercept := s.intercept
	s.intercept = nil
	s.mu.Unlock()
	if intercept != nil {
		intercept()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, found := s.rows[c.Slug]
	if c.Revision-1 != expected || found && old.Revision != expected || !found && expected != 0 {
		return ErrTaskControlConflict
	}
	fail := s.failVersion == c.Manifest.Version && s.failBlocked == (c.TransitionID != "")
	if fail {
		s.failVersion = ""
		if !s.failAfter {
			return ErrTaskControlUnavailable
		}
	}
	if s.rows == nil {
		s.rows = map[string]TaskControl{}
	}
	s.rows[c.Slug] = cloneTaskControl(c)
	if fail {
		return ErrTaskControlUnavailable
	}
	return nil
}

func taskControlFixture(t *testing.T, s *testTaskControls, code []byte) (*Manager, Manifest) {
	t.Helper()
	if code == nil {
		var err error
		code, err = os.ReadFile(filepath.Join("testdata", "host-bridge.wasm"))
		if err != nil {
			t.Fatal(err)
		}
	}
	manifest := Manifest{Schema: 1, Slug: "shared-host", Name: "Shared host", Version: "1.0.0",
		Runtime: "wasm/core-v1", Entry: "plugin.wasm", Capabilities: []string{"settings.read", "settings.write", "content.read", "cache"},
		Hooks:    map[string][]string{"admin.action": {"np_handle"}, "task.execute.v1": {"np_handle"}, "event.deliver.v1": {"np_handle"}},
		Settings: []Setting{{Key: "count", Label: "Count", Type: "number", Default: 0, Min: 0, Max: 10}}}
	m := NewManager(t.TempDir(), WithTaskControlStore(s))
	installed, err := m.InstallPackage(createPluginArchive(t, manifest, code))
	if err != nil {
		t.Fatal(err)
	}
	if err = WritePrivate(filepath.Join(m.root, manifest.Slug, ".official.json"), Release{Manifest: installed}); err != nil {
		t.Fatal(err)
	}
	return m, installed
}

func configureTaskControlFixture(t *testing.T, m *Manager, slug string) Configuration {
	t.Helper()
	manifest, c, _, err := m.Configuration(slug)
	if err != nil {
		t.Fatal(err)
	}
	c.Grants = manifest.Capabilities
	if err = m.Configure(slug, c); err != nil {
		t.Fatal(err)
	}
	if err = m.SetEnabled(slug, true); err != nil {
		t.Fatal(err)
	}
	_, c, _, err = m.Configuration(slug)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sharedControl(t *testing.T, s *testTaskControls, slug string) TaskControl {
	t.Helper()
	c, ok, err := s.LoadTaskControl(t.Context(), slug)
	if err != nil || !ok {
		t.Fatalf("shared control missing: %v", err)
	}
	return c
}

func TestSharedTaskControlRequiresExplicitConfiguration(t *testing.T) {
	s := &testTaskControls{}
	m, manifest := taskControlFixture(t, s, nil)
	dir := filepath.Join(m.root, manifest.Slug)
	local := defaultConfiguration(manifest)
	local.Version, local.Grants, local.Values["count"] = 55, manifest.Capabilities, float64(9)
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeConfiguration(filepath.Join(dir, "data", "runtime.json"), local); err != nil {
		t.Fatal(err)
	}
	if err := rotateTaskEpoch(filepath.Join(dir, "data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, ".disabled")); err != nil {
		t.Fatal(err)
	}
	_, c, _, err := m.Configuration(manifest.Slug)
	if err != nil || c.Version != 0 || len(c.Grants) != 0 || c.Values["count"] != float64(0) {
		t.Fatal("local grants were imported by a read", c, err)
	}
	if _, found, err := s.LoadTaskControl(t.Context(), manifest.Slug); err != nil || found {
		t.Fatal("read created shared authority", err)
	}
	if err := m.SetEnabled(manifest.Slug, true); err == nil {
		t.Fatal("enabled without a shared configuration save")
	}
	items, err := m.List()
	if err != nil || len(items) != 1 || items[0].Enabled {
		t.Fatal("local marker was shown as shared enabled", items, err)
	}
	configureTaskControlFixture(t, m, manifest.Slug)
	captured := sharedControl(t, s, manifest.Slug)
	if !captured.Enabled || captured.Configuration.Version != 1 || captured.Epoch.ActorID != 0 {
		t.Fatal("explicit save/enable did not create a fresh shared lifetime", captured)
	}
	called := false
	if err := m.WithTaskEpoch(manifest.Slug, captured.Epoch.Token, func() error { called = true; return nil }); !errors.Is(err, ErrInactive) || called {
		t.Fatal("local callback masqueraded as shared fence", err)
	}
}

func TestSharedTaskConfigurationCASAndGuestValues(t *testing.T) {
	s := &testTaskControls{}
	a, manifest := taskControlFixture(t, s, nil)
	b, _ := taskControlFixture(t, s, nil)
	c := configureTaskControlFixture(t, a, manifest.Slug)
	if err := b.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	before := sharedControl(t, s, manifest.Slug)
	out, err := a.Dispatch(t.Context(), manifest.Slug, BusinessInput{Event: "admin.action", Payload: json.RawMessage(`{"method":"settings.set","value":{"count":3}}`)}, nil)
	var response struct {
		OK bool `json:"ok"`
	}
	if err != nil || json.Unmarshal(out.Data, &response) != nil || !response.OK {
		t.Fatal("shared guest write failed", out, err)
	}
	after := sharedControl(t, s, manifest.Slug)
	if after.Epoch != before.Epoch || after.Configuration.Version != c.Version+1 || after.Configuration.Values["count"] != float64(3) {
		t.Fatal("guest value update rotated approval or lost its value", after)
	}
	_, seen, _, err := b.Configuration(manifest.Slug)
	if err != nil || seen.Version != after.Configuration.Version || seen.Values["count"] != float64(3) {
		t.Fatal("other directory read local values", seen, err)
	}
	if err := b.Configure(manifest.Slug, c); !errors.Is(err, ErrConfigurationConflict) || !sameTaskControl(sharedControl(t, s, manifest.Slug), after) {
		t.Fatal("stale configuration changed authority", err)
	}
	// Enabling rotates authority without changing configuration version. A
	// guest captured before that CAS must not write with the new approval.
	if err := b.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	stale := after.Configuration
	stale.Version++
	stale.Values["count"] = float64(4)
	code, err := os.ReadFile(filepath.Join(a.root, manifest.Slug, manifest.Entry))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.writeSharedTaskValues(t.Context(), manifest, code, before.Epoch.Token, stale); !errors.Is(err, ErrInactive) {
		t.Fatal("old guest wrote under a new approval", err)
	}
	if sharedControl(t, s, manifest.Slug).Configuration.Values["count"] != float64(3) {
		t.Fatal("stale guest mutated shared values")
	}
	if err := b.SetEnabled(manifest.Slug, false); err != nil {
		t.Fatal(err)
	}
	items, err := a.List()
	if err != nil || len(items) != 1 || items[0].Enabled {
		t.Fatal("remote disable was not reflected in list", items, err)
	}
	if _, err := a.CurrentTaskEpoch(manifest.Slug); !errors.Is(err, ErrInactive) {
		t.Fatal("remote disable retained active approval", err)
	}
}

func TestSharedTaskHostRevocationAndUnavailableDoNotCoolPlugin(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		name := "revoked"
		if unavailable {
			name = "storage_unavailable"
		}
		t.Run(name, func(t *testing.T) {
			s := &testTaskControls{}
			code := configurationSequenceModule([]byte(`{"method":"content.get","id":1}`), []byte(`{"method":"content.get","id":2}`))
			a, manifest := taskControlFixture(t, s, code)
			b, _ := taskControlFixture(t, s, code)
			configureTaskControlFixture(t, a, manifest.Slug)
			if err := b.SetEnabled(manifest.Slug, true); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				epoch, err := a.CurrentTaskEpoch(manifest.Slug)
				if err != nil {
					t.Fatal(err)
				}
				reads := 0
				executor := TaskExecutor{Manager: a, Authorize: func(context.Context, pluginjobs.Job) error { return nil },
					Read: func(context.Context, int64) (any, error) {
						reads++
						if unavailable {
							s.mu.Lock()
							s.loadError = ErrTaskControlUnavailable
							s.mu.Unlock()
						} else if err := b.SetEnabled(manifest.Slug, false); err != nil {
							t.Fatal(err)
						}
						return map[string]bool{"public": true}, nil
					}}
				job := pluginjobs.Job{ID: "test-claim", Plugin: manifest.Slug, Epoch: epoch.Token, ActorID: 1,
					Kind: "task.execute.v1", Status: "running", LeaseUntil: time.Now().Add(time.Minute), Payload: json.RawMessage(`{}`)}
				err = executor.Execute(t.Context(), job)
				want := pluginjobs.ErrAuthorization
				if unavailable {
					want = ErrTaskControlUnavailable
				}
				if !errors.Is(err, want) || reads != 1 || unavailable && errors.Is(err, pluginjobs.ErrAuthorization) {
					t.Fatal("guest ignored a failed host authorization", reads, err)
				}
				s.mu.Lock()
				s.loadError = nil
				s.mu.Unlock()
				if !unavailable {
					if err := b.SetEnabled(manifest.Slug, true); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, _, st, err := a.Configuration(manifest.Slug)
			if err != nil || st.Calls != 3 || st.Failures != 0 || st.Consecutive != 0 || !st.BlockedUntil.IsZero() {
				t.Fatal("host authority failure cooled a healthy plugin", st, err)
			}
		})
	}
}

func TestSharedTaskEnableRejectsInvalidMarkerBeforeCAS(t *testing.T) {
	s := &testTaskControls{}
	m, manifest := taskControlFixture(t, s, nil)
	_, c, _, err := m.Configuration(manifest.Slug)
	if err != nil || m.Configure(manifest.Slug, c) != nil {
		t.Fatal(err)
	}
	before := sharedControl(t, s, manifest.Slug)
	marker := filepath.Join(m.root, manifest.Slug, ".disabled")
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(manifest.Slug, true); err == nil || !sameTaskControl(before, sharedControl(t, s, manifest.Slug)) {
		t.Fatal("invalid local marker granted shared execution", err)
	}
}

func TestSharedTaskPackageTransitionsRecoverAmbiguousCommits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blocked bool
		after   bool
		version string
	}{
		{"blocking_response_lost", true, true, "1.0.0"},
		{"final_write_rejected", false, false, "1.1.0"},
		{"final_response_lost", false, true, "1.1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &testTaskControls{}
			m, manifest := taskControlFixture(t, s, nil)
			configureTaskControlFixture(t, m, manifest.Slug)
			old := sharedControl(t, s, manifest.Slug)
			code, err := os.ReadFile(filepath.Join(m.root, manifest.Slug, manifest.Entry))
			if err != nil {
				t.Fatal(err)
			}
			release, archive := upgradeRelease(t, manifest, code, "1.1.0")
			s.failVersion, s.failBlocked, s.failAfter = tc.version, tc.blocked, tc.after
			if err := m.updatePackage(t.Context(), archive, release, captureForTest(t, m, manifest.Slug), nil); !errors.Is(err, ErrTaskControlUnavailable) {
				t.Fatal("injection did not interrupt the transition", err)
			}
			if _, err := os.Stat(m.taskTransitionPath(manifest.Slug)); err != nil {
				t.Fatal("ambiguous intent was discarded", err)
			}
			if err := NewManager(m.root, WithTaskControlStore(s)).Recover(); err != nil {
				t.Fatal(err)
			}
			current := sharedControl(t, s, manifest.Slug)
			wantVersion := "1.1.0"
			if tc.blocked {
				wantVersion = "1.0.0"
			}
			if current.Enabled || current.Retired || current.TransitionID != "" || current.Manifest.Version != wantVersion || current.Epoch.Token == old.Epoch.Token {
				t.Fatal("recovery revived or replayed the wrong package", current)
			}
			installed, err := readManifest(filepath.Join(m.root, manifest.Slug, "plugin.json"))
			if err != nil || installed.Version != wantVersion {
				t.Fatal("shared and local package diverged", installed, err)
			}
			if _, err := os.Stat(filepath.Join(m.root, manifest.Slug, ".disabled")); err != nil {
				t.Fatal("recovery did not keep the local package disabled", err)
			}
			if _, err := os.Stat(m.taskTransitionPath(manifest.Slug)); !os.IsNotExist(err) {
				t.Fatal("completed intent remains", err)
			}
			before := current
			if err := m.Recover(); err != nil || !sameTaskControl(before, sharedControl(t, s, manifest.Slug)) {
				t.Fatal("repeated recovery changed shared authority", err)
			}
		})
	}
}

func TestSharedTaskTransitionLosingCASDoesNotReplay(t *testing.T) {
	s := &testTaskControls{}
	a, manifest := taskControlFixture(t, s, nil)
	b, _ := taskControlFixture(t, s, nil)
	configureTaskControlFixture(t, a, manifest.Slug)
	_, competing, _, err := b.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	competing.Values["count"] = float64(7)
	code, err := os.ReadFile(filepath.Join(a.root, manifest.Slug, manifest.Entry))
	if err != nil {
		t.Fatal(err)
	}
	release, archive := upgradeRelease(t, manifest, code, "1.1.0")
	s.intercept = func() {
		if err := b.Configure(manifest.Slug, competing); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.updatePackage(t.Context(), archive, release, captureForTest(t, a, manifest.Slug), nil); !errors.Is(err, ErrTaskControlConflict) {
		t.Fatal("competing configuration did not win", err)
	}
	before := sharedControl(t, s, manifest.Slug)
	if err := a.Recover(); err != nil || !sameTaskControl(before, sharedControl(t, s, manifest.Slug)) {
		t.Fatal("recovery overwrote a winning CAS", err)
	}
	if before.Manifest.Version != "1.0.0" || before.Configuration.Values["count"] != float64(7) || !before.Enabled {
		t.Fatal("winning configuration was changed", before)
	}
}

func TestSharedTaskRetirementAndUpgradeRequireFreshReview(t *testing.T) {
	s := &testTaskControls{}
	m, manifest := taskControlFixture(t, s, nil)
	configureTaskControlFixture(t, m, manifest.Slug)
	before := sharedControl(t, s, manifest.Slug)
	retired, err := NextTaskControl(before)
	if err != nil {
		t.Fatal(err)
	}
	retired.Enabled, retired.Retired = false, true
	retired.Epoch, err = freshTaskEpoch(0)
	if err != nil || s.SaveTaskControl(t.Context(), before.Revision, retired) != nil {
		t.Fatal(err)
	}
	code, err := os.ReadFile(filepath.Join(m.root, manifest.Slug, manifest.Entry))
	if err != nil {
		t.Fatal(err)
	}
	release, archive := upgradeRelease(t, manifest, code, "1.1.0")
	if err := m.updatePackage(t.Context(), archive, release, captureForTest(t, m, manifest.Slug), nil); err != nil {
		t.Fatal(err)
	}
	if current := sharedControl(t, s, manifest.Slug); !current.Retired || current.Enabled {
		t.Fatal("upgrade renewed restored approval", current)
	}
	if err := m.SetEnabled(manifest.Slug, true); err == nil {
		t.Fatal("upgrade bypassed mandatory configuration review")
	}
	if err := m.Uninstall(manifest.Slug); err != nil {
		t.Fatal(err)
	}
	current := sharedControl(t, s, manifest.Slug)
	if !current.Retired || current.Configuration.Version != 0 || len(current.Configuration.Grants) != 0 {
		t.Fatal("uninstall left restored configuration approved", current)
	}
}

func TestSharedTaskUninstallRecoversAfterSharedCommitFailure(t *testing.T) {
	s := &testTaskControls{}
	m, manifest := taskControlFixture(t, s, nil)
	configureTaskControlFixture(t, m, manifest.Slug)
	before := sharedControl(t, s, manifest.Slug)
	s.failVersion, s.failBlocked, s.failAfter = manifest.Version, false, false
	if err := m.Uninstall(manifest.Slug); !errors.Is(err, ErrTaskControlUnavailable) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.root, manifest.Slug)); !os.IsNotExist(err) {
		t.Fatal("local uninstall did not commit", err)
	}
	if err := m.Recover(); err != nil {
		t.Fatal(err)
	}
	current := sharedControl(t, s, manifest.Slug)
	if !current.Retired || current.Enabled || current.TransitionID != "" || current.Configuration.Version != 0 || current.Epoch.Token == before.Epoch.Token {
		t.Fatal("uninstall recovery did not retire shared identity", current)
	}
}

func TestSharedTaskPackageDigestAndInterruptedRollback(t *testing.T) {
	s := &testTaskControls{}
	m, manifest := taskControlFixture(t, s, nil)
	other, _ := taskControlFixture(t, s, nil)
	c := configureTaskControlFixture(t, m, manifest.Slug)
	c.Values["count"] = float64(4)
	if err := m.Configure(manifest.Slug, c); err != nil {
		t.Fatal(err)
	}
	if err := other.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	first := sharedControl(t, s, manifest.Slug)
	code, err := os.ReadFile(filepath.Join(m.root, manifest.Slug, manifest.Entry))
	if err != nil {
		t.Fatal(err)
	}
	release, archive := upgradeRelease(t, manifest, code, "1.1.0")
	if err := m.updatePackage(t.Context(), archive, release, captureForTest(t, m, manifest.Slug), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := other.CurrentTaskEpoch(manifest.Slug); !errors.Is(err, ErrInactive) {
		t.Fatal("another directory kept the old package authorized", err)
	}
	if err := m.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	upgraded := sharedControl(t, s, manifest.Slug)
	state, err := m.VersionState(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	s.failVersion, s.failBlocked, s.failAfter = manifest.Version, false, false
	if err := m.Rollback(t.Context(), manifest.Slug, "1.1.0", state.Revision, state.Previous, extension.Progress(nil)); !errors.Is(err, ErrTaskControlUnavailable) {
		t.Fatal("rollback interruption was not exercised", err)
	}
	if err := m.Recover(); err != nil {
		t.Fatal(err)
	}
	back := sharedControl(t, s, manifest.Slug)
	if back.Manifest.Version != manifest.Version || back.Enabled || back.Retired || back.TransitionID != "" || back.Epoch.Token == first.Epoch.Token || back.Epoch.Token == upgraded.Epoch.Token || back.Configuration.Values["count"] != float64(4) || back.Configuration.Version <= upgraded.Configuration.Version {
		t.Fatal("rollback revived old approval or lost shared values", back)
	}
	if _, err := os.Stat(filepath.Join(m.root, manifest.Slug, ".disabled")); err != nil {
		t.Fatal("recovered rollback was locally enabled", err)
	}
}

func TestSharedTaskSameManifestDifferentWasmRequiresReview(t *testing.T) {
	s := &testTaskControls{}
	firstCode := configurationSequenceModule([]byte(`{"method":"settings.get"}`))
	secondCode := configurationSequenceModule([]byte(`{"method":"settings.get"}`), []byte(`{"method":"settings.get"}`))
	a, manifest := taskControlFixture(t, s, firstCode)
	b, _ := taskControlFixture(t, s, secondCode)
	configureTaskControlFixture(t, a, manifest.Slug)
	if err := verifyIntegrity(filepath.Join(b.root, manifest.Slug), manifest, secondCode); err != nil {
		t.Fatal("alternate package fixture lacks its own valid receipt", err)
	}
	if err := os.Remove(filepath.Join(b.root, manifest.Slug, ".disabled")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CurrentTaskEpoch(manifest.Slug); !errors.Is(err, ErrInactive) {
		t.Fatal("matching manifest reused another module's grants", err)
	}
	if err := b.SetEnabled(manifest.Slug, true); err == nil {
		t.Fatal("different module enabled without explicit configuration save")
	}
	_, c, _, err := b.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Configure(manifest.Slug, c); err != nil {
		t.Fatal(err)
	}
	if current := sharedControl(t, s, manifest.Slug); current.Enabled || current.ModuleDigest != digestBytes(secondCode) {
		t.Fatal("explicit package review retained old execution", current)
	}
}

func TestSharedTaskRecoveryRequiresOriginalStoreAndKeepsBadIntent(t *testing.T) {
	s := &testTaskControls{}
	m, manifest := taskControlFixture(t, s, nil)
	configureTaskControlFixture(t, m, manifest.Slug)
	before := sharedControl(t, s, manifest.Slug)
	desired := before
	desired.Manifest.Version = "1.1.0"
	transition, err := m.beginTaskControlTransition(t.Context(), before, desired, "update")
	if err != nil {
		t.Fatal(err)
	}
	if err := NewManager(m.root).Recover(); err == nil {
		t.Fatal("local mode consumed a shared authorization intent")
	}
	path := m.taskTransitionPath(manifest.Slug)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append([]byte(`{"unexpected":true,`), raw[1:]...)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(); !errors.Is(err, ErrTaskControlCorrupt) {
		t.Fatal("malformed private intent was silently replayed", err)
	}
	if current := sharedControl(t, s, manifest.Slug); !sameTaskControl(current, transition.Blocked) {
		t.Fatal("bad intent changed shared authority", current)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("malformed intent evidence was removed", err)
	}
}
