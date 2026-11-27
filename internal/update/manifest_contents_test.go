package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCoreManifestRejectsMalformedSecondaryPlatforms(t *testing.T) {
	for _, test := range []struct {
		name, os, arch string
	}{
		{"empty OS", "", "amd64"},
		{"empty architecture", "other", ""},
		{"OS contains separator", "linux/other", "amd64"},
		{"architecture contains separator", "other", "arm/64"},
		{"OS contains whitespace", "other\n", "amd64"},
		{"oversized architecture", "other", strings.Repeat("a", 101)},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := Artifact{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "pkg.zip", Size: 10, SHA256: strings.Repeat("a", 64)}
			bad := artifact
			bad.OS, bad.Arch, bad.Path = test.os, test.arch, "secondary.zip"
			manifest := Manifest{Schema: manifestSchema, Version: "0.2.0", DataCompatible: true, Artifacts: []Artifact{artifact, bad}}
			if _, err := manifest.artifact(); err == nil {
				t.Error("malformed secondary platform was accepted alongside the current platform")
			}
		})
	}
}

func TestCoreManifestPreservesNonInstallableModes(t *testing.T) {
	for _, kind := range []string{"withdrawal", "offline", "foreign platform"} {
		t.Run(kind, func(t *testing.T) {
			manifest := Manifest{Schema: manifestSchema, Version: "0.2.0", DataCompatible: true, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "pkg.zip", Size: 10, SHA256: strings.Repeat("a", 64)}}}
			want := "没有"
			switch kind {
			case "withdrawal":
				manifest.DataCompatible, manifest.Artifacts, want = false, nil, "撤回"
			case "offline":
				manifest.DataCompatible, want = false, "离线"
			case "foreign platform":
				manifest.Artifacts[0].OS = "linux"
				if runtime.GOOS == "linux" {
					manifest.Artifacts[0].OS = "windows"
				}
			}
			if _, err := manifest.artifact(); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("non-installable mode was accepted or misclassified: %v", err)
			}
		})
	}
}

func TestMalformedSignedManifestRetainsSequenceProtection(t *testing.T) {
	for _, kind := range []string{"oversized notes", "offline malformed package"} {
		t.Run(kind, func(t *testing.T) {
			manager := managerFixture(t)
			key := fixtureKey(t)
			manifest := Manifest{Version: "0.2.0", DataCompatible: true, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "pkg.zip", Size: 10, SHA256: strings.Repeat("a", 64)}}}
			approved := signFixture(t, key, manifest, 1)
			if kind == "oversized notes" {
				manifest.Notes = strings.Repeat("n", 16001)
			} else {
				manifest.DataCompatible = false
				manifest.Artifacts[0].Path = "../pkg.zip"
			}
			malformed := signFixture(t, key, manifest, 2)
			var current atomic.Value
			current.Store(approved)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/commits/") {
					_ = json.NewEncoder(w).Encode(map[string]string{"sha": strings.Repeat("b", 40)})
					return
				}
				if !strings.HasSuffix(r.URL.Path, "stable.json") {
					t.Error("check tried to download a package")
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(current.Load().([]byte))
			}))
			defer server.Close()
			manager.client.base = server.URL
			if err := manager.Configure(testSource(), false); err != nil {
				t.Fatal(err)
			}
			for _, body := range [][]byte{approved, malformed, approved} {
				current.Store(body)
				if err := manager.Check(); err != nil {
					t.Fatal(err)
				}
				manager.wg.Wait()
			}
			public, err := publisherKey(testSource())
			if err != nil {
				t.Fatal(err)
			}
			var high trustCheckpoint
			if err := readJSON(filepath.Join(manager.root, "trust-"+digest(public)+".json"), &high); err != nil || high.Sequence != 2 {
				t.Errorf("authentic rejected publication lost sequence protection: %+v, %v", high, err)
			}
			view, err := manager.Snapshot()
			if err != nil || view.State.Phase != "failed" || view.Available != nil || view.Revision != "" || !strings.Contains(view.Discovery.Message, "序号") {
				t.Errorf("older publication became installable after malformed higher sequence: phase=%s, message=%s, %v", view.State.Phase, view.Discovery.Message, err)
			}
		})
	}
}
