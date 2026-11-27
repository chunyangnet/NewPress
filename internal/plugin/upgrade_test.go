package plugin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
)

func upgradeRelease(t *testing.T, manifest Manifest, code []byte, value string) (Release, string) {
	t.Helper()
	// Normalize numbers just as a signed JSON catalog does.
	raw, _ := json.Marshal(manifest)
	var next Manifest
	if err := json.Unmarshal(raw, &next); err != nil {
		t.Fatal(err)
	}
	next.Version = value
	name := createPluginArchive(t, next, code)
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return Release{Manifest: next, Path: "packages/" + next.Slug + "-" + value + ".zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b)), Release: &extension.Metadata{Channel: "stable", PublishedAt: time.Now().UTC(), DataCompatible: true}}, name
}

func captureForTest(t *testing.T, m *Manager, slug string) updateTarget {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	target, err := m.captureUpdate(slug, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestPluginOTAUpdateAndRollbackPreserveLiveDataAndCancelOldGeneration(t *testing.T) {
	m, manifest, code := businessFixture(t)
	_, configuration, _, _ := m.Configuration(manifest.Slug)
	configuration.Values["title"] = "站点自己的标题"
	configuration.Values["days"] = float64(21)
	if err := m.Configure(manifest.Slug, configuration); err != nil {
		t.Fatal(err)
	}
	if err := m.WithData(manifest.Slug, true, 0, func(dir string, _ uint64) error {
		return WritePrivate(filepath.Join(dir, "private.json"), map[string]string{"value": "preserve-this-private-data"})
	}); err != nil {
		t.Fatal(err)
	}
	target := captureForTest(t, m, manifest.Slug)
	lease, err := m.ActiveContext(manifest.Slug, target.generation)
	if err != nil {
		t.Fatal(err)
	}
	release, archive := upgradeRelease(t, manifest, code, "1.1.0")
	if err := m.updatePackage(t.Context(), archive, release, target, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lease.Done():
	default:
		t.Fatal("old generation remains active")
	}
	items, err := m.List()
	if err != nil || len(items) != 1 || items[0].Manifest.Version != "1.1.0" || !items[0].Enabled || items[0].VersionState.ReviewRequired {
		t.Fatal(items, err)
	}
	_, saved, _, err := m.Configuration(manifest.Slug)
	if err != nil || saved.Values["title"] != configuration.Values["title"] || saved.Values["days"] != float64(21) || !slices.Equal(saved.Grants, configuration.Grants) {
		t.Fatal("configuration or grants changed", saved, err)
	}
	if err := m.WithData(manifest.Slug, true, target.generation, func(string, uint64) error { t.Fatal("stale write ran"); return nil }); !errors.Is(err, ErrInactive) {
		t.Fatal("stale generation accepted", err)
	}
	out, err := m.Dispatch(t.Context(), manifest.Slug, BusinessInput{Event: "admin.action", Payload: json.RawMessage(`{"action":"preview"}`)}, nil)
	if err != nil || out.Fragment == nil || out.Fragment.Title != "站点自己的标题" {
		t.Fatal(out, err)
	}
	// Writes made after upgrading must survive a later code rollback.
	saved.Values["title"] = "更新后保存的标题"
	if err := m.Configure(manifest.Slug, saved); err != nil {
		t.Fatal(err)
	}
	state, _ := m.VersionState(manifest.Slug)
	if err := m.Rollback(t.Context(), manifest.Slug, "1.1.0", state.Revision, "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	rolled, settings, _, err := m.Configuration(manifest.Slug)
	if err != nil || rolled.Version != "1.0.0" || settings.Values["title"] != "更新后保存的标题" {
		t.Fatal(rolled.Version, settings, err)
	}
	state, _ = m.VersionState(manifest.Slug)
	if len(state.History) != 2 || state.History[1].Action != "rollback" || state.Previous != "1.1.0" {
		t.Fatal(state)
	}
	private, err := ReadPrivate[map[string]string](filepath.Join(m.root, manifest.Slug, "data", "private.json"), 1024)
	if err != nil || private["value"] != "preserve-this-private-data" {
		t.Fatal("private data lost", err)
	}
}

func TestPluginOTANewPermissionsAndConfigurationRequireReview(t *testing.T) {
	m, manifest, code := businessFixture(t)
	target := captureForTest(t, m, manifest.Slug)
	manifest.Capabilities = append(manifest.Capabilities, "cache", "http.outbound")
	manifest.OutboundHosts = []string{"api.example.com"}
	manifest.Settings = append(manifest.Settings, Setting{Key: "subtitle", Label: "副标题", Type: "text", Default: "新默认值", Max: 80})
	release, archive := upgradeRelease(t, manifest, code, "1.1.0")
	if err := m.updatePackage(t.Context(), archive, release, target, nil); err != nil {
		t.Fatal(err)
	}
	items, _ := m.List()
	if items[0].Enabled || !items[0].VersionState.ReviewRequired {
		t.Fatal("new permissions silently activated")
	}
	_, configuration, _, err := m.Configuration(manifest.Slug)
	if err != nil || configuration.Values["subtitle"] != "新默认值" || slices.Contains(configuration.Grants, "cache") || slices.Contains(configuration.Grants, "http.outbound") {
		t.Fatal(configuration, err)
	}
	if err := m.SetEnabled(manifest.Slug, true); err == nil {
		t.Fatal("enabled without reapproval")
	}
	configuration.Grants = release.Manifest.Capabilities
	if err := m.Configure(manifest.Slug, configuration); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
}

func TestPluginOTAUnconfiguredInstallationStillRequiresConfirmation(t *testing.T) {
	_, manifest, code := businessFixture(t)
	m := NewManager(t.TempDir())
	if _, err := m.InstallPackage(createPluginArchive(t, manifest, code)); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(filepath.Join(m.root, manifest.Slug, ".official.json"), Release{Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	release, archive := upgradeRelease(t, manifest, code, "1.1.0")
	if err := m.updatePackage(t.Context(), archive, release, captureForTest(t, m, manifest.Slug), nil); err != nil {
		t.Fatal(err)
	}
	state, err := m.VersionState(manifest.Slug)
	if err != nil || !state.ReviewRequired {
		t.Fatal("unconfirmed settings became confirmed through update", state, err)
	}
	if err := m.SetEnabled(manifest.Slug, true); err == nil {
		t.Fatal("unconfigured plugin enabled after update")
	}
	_, configuration, _, err := m.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Configure(manifest.Slug, configuration); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
}

func TestPluginOTAFailuresKeepOriginalVersionAndSettings(t *testing.T) {
	for _, failure := range []string{"configuration", "runtime", "stale-generation", "cancel-at-switch", "incompatible-core", "not-data-compatible", "same-version", "downgrade"} {
		t.Run(failure, func(t *testing.T) {
			m, manifest, code := businessFixture(t)
			target := captureForTest(t, m, manifest.Slug)
			value := "1.1.0"
			switch failure {
			case "configuration":
				manifest.Settings[0].Max = 1
				manifest.Settings[0].Default = float64(1)
			case "runtime":
				code = []byte("not wasm")
			case "incompatible-core":
				manifest.NewPress = ">=99.0.0"
			case "same-version":
				value = "1.0.0"
			case "downgrade":
				value = "0.9.0"
			}
			release, archive := upgradeRelease(t, manifest, code, value)
			if failure == "not-data-compatible" {
				release.Release.DataCompatible = false
			}
			if failure == "stale-generation" {
				_, cfg, _, _ := m.Configuration(manifest.Slug)
				cfg.Values["title"] = "并发保存"
				if err := m.Configure(manifest.Slug, cfg); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			progress := extension.Progress(func(phase string, _, _ int64) {
				if failure == "cancel-at-switch" && phase == "switching" {
					cancel()
				}
			})
			if err := m.updatePackage(ctx, archive, release, target, progress); err == nil {
				t.Fatal("invalid update accepted")
			}
			items, err := m.List()
			if err != nil || items[0].Manifest.Version != "1.0.0" || !items[0].Enabled || items[0].VersionState.Revision != target.state.Revision {
				t.Fatal("failed update changed installation", items, err)
			}
			pending, _ := filepath.Glob(filepath.Join(m.root, ".ota-*"))
			if len(pending) != 0 {
				t.Fatal("failed preparation left transaction", pending)
			}
		})
	}
}

func TestPluginOTACorruptRollbackIsRejected(t *testing.T) {
	m, manifest, code := businessFixture(t)
	release, archive := upgradeRelease(t, manifest, code, "1.1.0")
	if err := m.updatePackage(t.Context(), archive, release, captureForTest(t, m, manifest.Slug), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.root, manifest.Slug, extension.RollbackDir, "plugin.wasm"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	state, _ := m.VersionState(manifest.Slug)
	if err := m.Rollback(t.Context(), manifest.Slug, "1.1.0", state.Revision, "1.0.0", nil); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
	items, _ := m.List()
	if items[0].Manifest.Version != "1.1.0" {
		t.Fatal("failed rollback changed version")
	}
}

func TestSignedOTADownloadBindsExpectedInstallationBeforeNetwork(t *testing.T) {
	for _, changeDuringDownload := range []bool{false, true} {
		t.Run(map[bool]string{false: "update", true: "stale"}[changeDuringDownload], func(t *testing.T) {
			m, manifest, code := businessFixture(t)
			release, archive := upgradeRelease(t, manifest, code, "1.1.0")
			payload, _ := os.ReadFile(archive)
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			market, err := NewMarket(m, MarketSource{[]string{githubCatalog}, base64.StdEncoding.EncodeToString(pub)})
			if err != nil {
				t.Fatal(err)
			}
			catalog := signCatalog(t, Catalog{Schema: 2, Sequence: 2, Expires: time.Now().Add(time.Hour), Plugins: []Release{release}}, key)
			market.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
				b := catalog
				if strings.HasSuffix(r.URL.Path, ".zip") {
					b = payload
					if changeDuringDownload {
						_, cfg, _, _ := m.Configuration(manifest.Slug)
						cfg.Values["title"] = "下载期间的修改"
						if err := m.Configure(manifest.Slug, cfg); err != nil {
							t.Fatal(err)
						}
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b))}, nil
			})}
			if err := market.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			state, _ := m.VersionState(manifest.Slug)
			var downloaded int64
			err = market.Update(t.Context(), manifest.Slug, release.SHA256, "1.0.0", state.Revision, func(phase string, received, total int64) {
				if phase == "downloading" {
					downloaded = received
					if total != release.Size {
						t.Error("incorrect download size")
					}
				}
			})
			if changeDuringDownload {
				if !errors.Is(err, extension.ErrConflict) {
					t.Fatal("stale generation not rejected", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if downloaded != release.Size {
				t.Fatal("download progress not measured")
			}
			items, _ := m.List()
			expected := "1.1.0"
			if changeDuringDownload {
				expected = "1.0.0"
			}
			if items[0].Manifest.Version != expected {
				t.Fatal(items[0].Manifest.Version)
			}
		})
	}
}

func TestOfficialCatalogNeverFallsBackToRetiredCatalog(t *testing.T) {
	for _, status := range []int{404, 200, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			market, err := NewMarket(NewManager(t.TempDir()), officialSource())
			if err != nil {
				t.Fatal(err)
			}
			var legacyCalls int
			var mu sync.Mutex
			market.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
				code := status
				if strings.Contains(r.URL.Path, "runtime-v1") {
					mu.Lock()
					legacyCalls++
					mu.Unlock()
					code = 200
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(`{"payload":"invalid","signature":"invalid"}`))}, nil
			})}
			if err := market.Refresh(t.Context()); err == nil {
				t.Fatal("invalid signature accepted")
			}
			want := 0
			if legacyCalls != want {
				t.Fatalf("fallback calls=%d, want %d", legacyCalls, want)
			}
		})
	}
}
