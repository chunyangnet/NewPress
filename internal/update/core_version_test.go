package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCoreManifestVersionBoundaries(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "core_versions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Version string
		Valid         bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			manifest := Manifest{Schema: manifestSchema, Version: test.Version, DataCompatible: true, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "pkg.zip", Size: 10, SHA256: strings.Repeat("a", 64)}}}
			if _, err := manifest.artifact(); (err == nil) != test.Valid {
				t.Errorf("manifest version %q: accepted=%v, want %v", test.Version, err == nil, test.Valid)
			}
			if !test.Valid {
				release := Release{ID: strings.Repeat("a", 32), Version: test.Version, BinaryHash: strings.Repeat("b", 64)}
				if err := validateRelease(t.TempDir(), "unused", release, true); err == nil || err.Error() != "发布记录无效" {
					t.Errorf("invalid version reached executable inspection: %v", err)
				}
			}
		})
	}
}

func TestCoreVersionPrecedenceDoesNotOverflow(t *testing.T) {
	for _, test := range []struct {
		name, candidate, current string
		want                     bool
	}{
		{"large major upgrade", "18446744073709551616.0.0", "18446744073709551615.0.0", true},
		{"large major downgrade", "18446744073709551615.0.0", "18446744073709551616.0.0", false},
		{"large minor upgrade", "1.18446744073709551616.0", "1.18446744073709551615.0", true},
		{"large patch upgrade", "1.0.18446744073709551616", "1.0.18446744073709551615", true},
		{"large prerelease upgrade", "1.0.0-1000000000000000000000", "1.0.0-999999999999999999999", true},
		{"large prerelease downgrade", "1.0.0-999999999999999999999", "1.0.0-1000000000000000000000", false},
		{"numeric before hyphen identifier", "1.0.0-18446744073709551616", "1.0.0--", false},
		{"invalid candidate leading zero", "1.0.0-01", "1.0.0-0", false},
		{"invalid current leading zero", "1.0.0", "1.0.0-01", false},
		{"empty prerelease identifier", "1.0.0-rc..1", "1.0.0-rc", false},
		{"stable after prerelease", "1.0.0", "1.0.0-rc.1", true},
		{"legacy prefix same version", "v1.0.0", "1.0.0", false},
		{"legacy prefix upgrade", "v1.0.1", "1.0.0", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := newer(test.candidate, test.current); got != test.want {
				t.Errorf("newer(%q, %q) = %v, want %v", test.candidate, test.current, got, test.want)
			}
		})
	}
}

func TestCoreUpdateCheckUsesSemanticVersion(t *testing.T) {
	for _, test := range []struct {
		name, candidate, current, phase string
	}{
		{"large major upgrade", "18446744073709551616.0.0", "18446744073709551615.0.0", "available"},
		{"large numeric prerelease downgrade", "1.0.0-999999999999999999999", "1.0.0-1000000000000000000000", "current"},
		{"invalid prerelease", "1.0.0-rc..1", "0.1.0", "failed"},
		{"legacy prefix upgrade", "v1.0.1", "1.0.0", "available"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := managerFixture(t)
			manager.current = test.current
			key := fixtureKey(t)
			signed := signFixture(t, key, Manifest{Version: test.candidate, DataCompatible: true, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "pkg.zip", Size: 10, SHA256: strings.Repeat("a", 64)}}}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/commits/") {
					_ = json.NewEncoder(w).Encode(map[string]string{"sha": strings.Repeat("b", 40)})
					return
				}
				_, _ = w.Write(signed)
			}))
			defer server.Close()
			manager.client.base = server.URL
			if err := manager.Configure(testSource(), false); err != nil {
				t.Fatal(err)
			}
			if err := manager.Check(); err != nil {
				t.Fatal(err)
			}
			manager.wg.Wait()
			view, err := manager.Snapshot()
			if err != nil || view.State.Phase != test.phase {
				t.Errorf("checked phase=%s, want %s: %v", view.State.Phase, test.phase, err)
			}
			if test.phase == "available" {
				if view.Available == nil || view.Available.Version != test.candidate || view.Revision == "" {
					t.Error("valid upgrade did not produce an installable candidate")
				}
			} else if view.Available != nil || view.Revision != "" {
				t.Error("invalid version or downgrade produced an installable candidate")
			}
		})
	}
}
