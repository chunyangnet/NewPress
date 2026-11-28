package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"newpress.local/newpress/internal/testwasm"
)

func configurationLimitFixture(t *testing.T, fields int) (*Manager, Manifest, []byte) {
	t.Helper()
	code, err := os.ReadFile(filepath.Join("testdata", "host-bridge.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Schema: 1, Slug: "settings-boundary", Name: "Settings boundary", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm",
		Capabilities: []string{"settings.read", "settings.write", "content.read", "render.fragment", "cache", "http.outbound"},
		Hooks:        map[string][]string{"admin.action": {"np_handle"}, "content.render": {"np_handle"}},
	}
	for i := range fields {
		manifest.Settings = append(manifest.Settings, Setting{Key: fmt.Sprintf("field_%02d", i), Label: "Text", Type: "text", Default: "", Max: 4000})
	}
	m := NewManager(t.TempDir())
	manifest, err = m.InstallPackage(createPluginArchive(t, manifest, code))
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(filepath.Join(m.root, manifest.Slug, ".official.json"), Release{Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	config := defaultConfiguration(manifest)
	config.Grants = slices.Clone(manifest.Capabilities)
	if err := m.Configure(manifest.Slug, config); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	return m, manifest, code
}

func configurationLimitSnapshot(t *testing.T, m *Manager, slug string) (Configuration, []byte) {
	t.Helper()
	_, config, _, err := m.Configuration(slug)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(m.root, slug, "data", "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	return config, data
}

func assertConfigurationLimitPreserved(t *testing.T, m *Manager, slug string, config Configuration, original []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(m.root, slug, "data", "runtime.json"))
	if err != nil || !bytes.Equal(data, original) {
		t.Errorf("rejected operation replaced configuration: bytes=%d err=%v", len(data), err)
	}
	_, restarted, _, err := NewManager(m.root).Configuration(slug)
	if err != nil {
		t.Errorf("new manager cannot reload configuration: %v", err)
	} else if !reflect.DeepEqual(restarted, config) {
		t.Error("reloaded values, grants or version changed")
	}
}

func expectConfigurationLimitError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Error("accepted a configuration beyond the restart reader's 64 KiB limit")
	} else if !strings.Contains(err.Error(), "64 KiB") {
		t.Errorf("expected actionable size error, got %v", err)
	}
}

// Fill independent text fields to an exact serialized size, counting the next
// version. Every individual field remains within its declared character limit.
func configurationAtBytes(t *testing.T, config Configuration, target int) Configuration {
	t.Helper()
	config.Values = maps.Clone(config.Values)
	for key := range config.Values {
		config.Values[key] = ""
	}
	next := config
	next.Version++
	data, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	remaining := target - len(data)
	for _, key := range slices.Sorted(maps.Keys(config.Values)) {
		n := min(4000, remaining)
		config.Values[key] = strings.Repeat("x", n)
		remaining -= n
	}
	if remaining != 0 {
		t.Fatal("fixture cannot reach configuration boundary")
	}
	return config
}

func TestPluginConfigurationRejectsUnreadableWrites(t *testing.T) {
	for _, escaped := range []bool{false, true} {
		name, count, character := "aggregate", 32, "x"
		if escaped {
			name, count, character = "json_escaping", 3, "&"
		}
		t.Run(name, func(t *testing.T) {
			m, manifest, _ := configurationLimitFixture(t, count)
			before, data := configurationLimitSnapshot(t, m, manifest.Slug)
			target := captureForTest(t, m, manifest.Slug)
			lease, err := m.ActiveContext(manifest.Slug, target.generation)
			if err != nil {
				t.Fatal(err)
			}
			candidate := before
			candidate.Values = maps.Clone(before.Values)
			for _, field := range manifest.Settings {
				candidate.Values[field.Key] = strings.Repeat(character, 4000)
				if err := validateValue(field, candidate.Values[field.Key]); err != nil {
					t.Fatal(err)
				}
			}
			encoded, err := json.Marshal(candidate)
			if err != nil || len(encoded) <= 64<<10 {
				t.Fatal("fixture must exceed total size without violating field limits")
			}
			t.Logf("individually valid configuration: %d bytes", len(encoded))
			expectConfigurationLimitError(t, m.Configure(manifest.Slug, candidate))
			assertConfigurationLimitPreserved(t, m, manifest.Slug, before, data)
			if lease.Err() != nil || captureForTest(t, m, manifest.Slug).generation != target.generation {
				t.Error("rejected save cancelled the active generation")
			}
			before.Values[manifest.Settings[0].Key] = "corrected"
			if err := m.Configure(manifest.Slug, before); err != nil {
				t.Fatalf("valid retry failed: %v", err)
			}
		})
	}
}

func TestPluginConfigurationSerializedBoundary(t *testing.T) {
	for _, excess := range []int{0, 1} {
		t.Run(fmt.Sprintf("excess_%d", excess), func(t *testing.T) {
			m, manifest, _ := configurationLimitFixture(t, 17)
			config, data := configurationLimitSnapshot(t, m, manifest.Slug)
			for config.Version < 9 {
				if err := m.Configure(manifest.Slug, config); err != nil {
					t.Fatal(err)
				}
				config, data = configurationLimitSnapshot(t, m, manifest.Slug)
			}
			candidate := configurationAtBytes(t, config, (64<<10)+excess)
			err := m.Configure(manifest.Slug, candidate)
			if excess > 0 {
				expectConfigurationLimitError(t, err)
				assertConfigurationLimitPreserved(t, m, manifest.Slug, config, data)
				return
			}
			if err != nil {
				t.Fatalf("exactly 64 KiB should remain readable: %v", err)
			}
			saved, encoded := configurationLimitSnapshot(t, NewManager(m.root), manifest.Slug)
			if len(encoded) != 64<<10 || saved.Version != 10 || !reflect.DeepEqual(saved.Values, candidate.Values) {
				t.Fatalf("boundary round trip: bytes=%d version=%d", len(encoded), saved.Version)
			}
		})
	}
}

func TestPluginHostSettingsRejectsUnreadableWrite(t *testing.T) {
	m, manifest, _ := configurationLimitFixture(t, 17)
	before, data := configurationLimitSnapshot(t, m, manifest.Slug)
	candidate := configurationAtBytes(t, before, (64<<10)+1)
	payload, err := json.Marshal(map[string]any{"method": "settings.set", "value": candidate.Values})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) >= businessBytes {
		t.Fatalf("fixture must fit the host request limit: bytes=%d", len(payload))
	}
	// A static guest exercises the real host boundary without parsing 64 KiB
	// through the fixture's generic JSON bridge inside the 250ms sandbox budget.
	dispatch := func(request []byte) (BusinessResult, error) {
		t.Helper()
		code := configurationSequenceModule(request)
		if err := os.WriteFile(filepath.Join(m.root, manifest.Slug, "plugin.wasm"), code, 0600); err != nil {
			t.Fatal(err)
		}
		if err := recordIntegrity(filepath.Join(m.root, manifest.Slug), manifest); err != nil {
			t.Fatal(err)
		}
		return m.Dispatch(t.Context(), manifest.Slug, BusinessInput{Event: "admin.action"}, nil)
	}
	t.Logf("Host request=%d bytes; persisted configuration would be 65537 bytes", len(payload))
	result, err := dispatch(payload)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(result.Data, &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || !strings.Contains(response.Error, "64 KiB") {
		t.Errorf("host must reject oversized configuration: %+v", response)
	}
	assertConfigurationLimitPreserved(t, m, manifest.Slug, before, data)
	before.Values["field_00"] = "valid host retry"
	payload, err = json.Marshal(map[string]any{"method": "settings.set", "value": before.Values})
	if err != nil {
		t.Fatal(err)
	}
	result, err = dispatch(payload)
	if err != nil {
		t.Fatal(err)
	}
	response.OK = false
	if err := json.Unmarshal(result.Data, &response); err != nil || !response.OK {
		t.Fatalf("valid host retry failed: %s %v", result.Data, err)
	}
	saved, _ := configurationLimitSnapshot(t, NewManager(m.root), manifest.Slug)
	if saved.Version != before.Version+1 || !reflect.DeepEqual(saved.Values, before.Values) {
		t.Fatal("valid host write did not survive restart")
	}
}

// Build a real guest that makes several host calls in a single invocation and
// returns the final response. This checks request-local state after a rejected
// write without depending on an external C/WASM compiler.
func configurationSequenceModule(requests ...[]byte) []byte {
	return testwasm.HostSequence(requests...)
}

func TestPluginHostRejectedSettingsKeepInvocationState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		retry  bool
		schema bool
	}{
		{"read_after_size_rejection", false, false},
		{"retry_after_size_rejection", true, false},
		{"read_after_schema_rejection", false, true},
		{"retry_after_schema_rejection", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, manifest, _ := configurationLimitFixture(t, 17)
			before, data := configurationLimitSnapshot(t, m, manifest.Slug)
			candidate := configurationAtBytes(t, before, (64<<10)+1)
			if tc.schema {
				candidate.Values = maps.Clone(before.Values)
				candidate.Values["field_00"] = 123
			}
			first, err := json.Marshal(map[string]any{"method": "settings.set", "value": candidate.Values})
			if err != nil {
				t.Fatal(err)
			}
			last := []byte(`{"method":"settings.get"}`)
			if tc.retry {
				last, err = json.Marshal(map[string]any{"method": "settings.set", "value": before.Values})
				if err != nil {
					t.Fatal(err)
				}
			}
			code := configurationSequenceModule(first, last)
			if err := os.WriteFile(filepath.Join(m.root, manifest.Slug, "plugin.wasm"), code, 0600); err != nil {
				t.Fatal(err)
			}
			if err := recordIntegrity(filepath.Join(m.root, manifest.Slug), manifest); err != nil {
				t.Fatal(err)
			}
			result, err := m.Dispatch(t.Context(), manifest.Slug, BusinessInput{Event: "admin.action"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				OK    bool            `json:"ok"`
				Data  json.RawMessage `json:"data"`
				Error string          `json:"error"`
			}
			if err := json.Unmarshal(result.Data, &response); err != nil || !response.OK {
				t.Fatalf("later host call in same invocation failed: %s %v", response.Error, err)
			}
			if tc.retry {
				saved, _ := configurationLimitSnapshot(t, NewManager(m.root), manifest.Slug)
				if saved.Version != before.Version+1 || !reflect.DeepEqual(saved.Values, before.Values) {
					t.Fatal("rejected write consumed a version or altered valid retry")
				}
			} else {
				var values map[string]any
				if err := json.Unmarshal(response.Data, &values); err != nil || !reflect.DeepEqual(values, before.Values) {
					t.Errorf("settings.get exposed uncommitted values: %v", err)
				}
				assertConfigurationLimitPreserved(t, m, manifest.Slug, before, data)
			}
		})
	}
}

func TestPluginHostSettingsSuccessfulWritesAdvanceInvocationVersion(t *testing.T) {
	m, manifest, _ := configurationLimitFixture(t, 1)
	before, _ := configurationLimitSnapshot(t, m, manifest.Slug)
	code := configurationSequenceModule(
		[]byte(`{"method":"settings.set","value":{"field_00":"first"}}`),
		[]byte(`{"method":"settings.set","value":{"field_00":"second"}}`),
		[]byte(`{"method":"settings.get"}`),
	)
	if err := os.WriteFile(filepath.Join(m.root, manifest.Slug, "plugin.wasm"), code, 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordIntegrity(filepath.Join(m.root, manifest.Slug), manifest); err != nil {
		t.Fatal(err)
	}
	result, err := m.Dispatch(t.Context(), manifest.Slug, BusinessInput{Event: "admin.action"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		OK   bool              `json:"ok"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(result.Data, &response); err != nil || !response.OK || response.Data["field_00"] != "second" {
		t.Fatalf("same-invocation writes did not commit in order: %s %v", result.Data, err)
	}
	saved, _ := configurationLimitSnapshot(t, NewManager(m.root), manifest.Slug)
	if saved.Version != before.Version+2 || saved.Values["field_00"] != "second" {
		t.Fatal("successful host writes did not persist separate versions")
	}
}

func TestPluginConfigurationVersionOverflowPreservesState(t *testing.T) {
	for _, host := range []bool{false, true} {
		name := "administrator"
		if host {
			name = "host"
		}
		t.Run(name, func(t *testing.T) {
			m, manifest, _ := configurationLimitFixture(t, 1)
			config, _ := configurationLimitSnapshot(t, m, manifest.Slug)
			config.Version = math.MaxInt64
			if err := WritePrivate(filepath.Join(m.root, manifest.Slug, "data", "runtime.json"), config); err != nil {
				t.Fatal(err)
			}
			config, original := configurationLimitSnapshot(t, m, manifest.Slug)
			if host {
				result, err := m.Dispatch(t.Context(), manifest.Slug, BusinessInput{Event: "admin.action", Payload: json.RawMessage(`{"method":"settings.set","value":{"field_00":"changed"}}`)}, nil)
				if err != nil {
					t.Fatal(err)
				}
				var response struct {
					OK    bool   `json:"ok"`
					Error string `json:"error"`
				}
				if err := json.Unmarshal(result.Data, &response); err != nil || response.OK || !strings.Contains(response.Error, "版本") {
					t.Errorf("host accepted version overflow: %s %v", result.Data, err)
				}
			} else if err := m.Configure(manifest.Slug, config); err == nil || !strings.Contains(err.Error(), "版本") {
				t.Errorf("administrator save accepted version overflow: %v", err)
			}
			assertConfigurationLimitPreserved(t, m, manifest.Slug, config, original)
		})
	}
}

func TestPluginOTAMergedConfigurationLimit(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name, fields := "update", 8
		if rollback {
			name, fields = "rollback", 17
		}
		t.Run(name, func(t *testing.T) {
			m, manifest, code := configurationLimitFixture(t, fields)
			config, _ := configurationLimitSnapshot(t, m, manifest.Slug)
			for _, field := range manifest.Settings {
				if rollback && field.Key < "field_08" {
					continue
				}
				config.Values[field.Key] = strings.Repeat("x", 4000)
			}
			if err := m.Configure(manifest.Slug, config); err != nil {
				t.Fatal(err)
			}
			next := manifest
			next.Settings = slices.Clone(manifest.Settings)
			if rollback {
				next.Settings = next.Settings[:8]
			} else {
				for i := 8; i < 17; i++ {
					next.Settings = append(next.Settings, Setting{Key: fmt.Sprintf("field_%02d", i), Label: "New text", Type: "text", Default: strings.Repeat("y", 4000), Max: 4000})
				}
			}
			release, archive := upgradeRelease(t, next, code, "1.1.0")
			if rollback {
				if err := m.updatePackage(t.Context(), archive, release, captureForTest(t, m, manifest.Slug), nil); err != nil {
					t.Fatal(err)
				}
				config, _ = configurationLimitSnapshot(t, m, manifest.Slug)
				for key := range config.Values {
					config.Values[key] = strings.Repeat("y", 4000)
				}
				if err := m.Configure(manifest.Slug, config); err != nil {
					t.Fatal(err)
				}
				if err := m.SetEnabled(manifest.Slug, true); err != nil {
					t.Fatal(err)
				}
			}
			config, before := configurationLimitSnapshot(t, m, manifest.Slug)
			target := captureForTest(t, m, manifest.Slug)
			lease, err := m.ActiveContext(manifest.Slug, target.generation)
			if err != nil {
				t.Fatal(err)
			}
			operation := func() error {
				if rollback {
					return m.Rollback(t.Context(), manifest.Slug, target.manifest.Version, target.state.Revision, "1.0.0", nil)
				}
				return m.updatePackage(t.Context(), archive, release, target, nil)
			}
			expectConfigurationLimitError(t, operation())
			assertConfigurationLimitPreserved(t, m, manifest.Slug, config, before)
			current := captureForTest(t, m, manifest.Slug)
			if current.manifest.Version != target.manifest.Version || current.state.Revision != target.state.Revision || current.generation != target.generation || lease.Err() != nil {
				t.Error("rejected merge changed the installed version, history or active generation")
			}
			pending, err := filepath.Glob(filepath.Join(m.root, ".ota-*"))
			if err != nil || len(pending) != 0 {
				t.Errorf("rejected merge left transaction files: %v %v", pending, err)
			}
			for key := range config.Values {
				config.Values[key] = "reduced"
			}
			if err := m.Configure(manifest.Slug, config); err != nil {
				t.Fatalf("cannot correct configuration after rejected merge: %v", err)
			}
			target = captureForTest(t, m, manifest.Slug)
			if err := operation(); err != nil {
				t.Fatalf("valid merge retry failed: %v", err)
			}
			saved, _ := configurationLimitSnapshot(t, NewManager(m.root), manifest.Slug)
			if saved.Values["field_00"] != "reduced" || len(saved.Values) != 17 {
				t.Fatal("successful merge lost compatible values")
			}
			wantRecovered := strings.Repeat("y", 4000)
			if rollback {
				wantRecovered = strings.Repeat("x", 4000)
			}
			if saved.Values["field_16"] != wantRecovered {
				t.Fatal("successful merge lost defaults or rollback snapshot values")
			}
		})
	}
}
