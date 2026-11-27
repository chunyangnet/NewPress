package plugin

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeFileAcceptsABIv1Module(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "plugin.wasm")
	if err := os.WriteFile(path, abiV1Module(), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ProbeFile(context.Background(), path, "test-plugin", DefaultSandboxOptions())
	if err != nil {
		t.Fatalf("probe valid plugin: %v", err)
	}
	if result.ABIVersion != ABIVersion {
		t.Fatalf("ABI = %d, want %d", result.ABIVersion, ABIVersion)
	}
}

func TestProbeFileRejectsNonWasm(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "plugin.wasm")
	if err := os.WriteFile(path, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeFile(context.Background(), path, "bad-plugin", DefaultSandboxOptions()); err == nil {
		t.Fatal("expected invalid WebAssembly to be rejected")
	}
}

func TestProbeFileRejectsSymlinkEntry(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	target := filepath.Join(directory, "target.wasm")
	if err := os.WriteFile(target, abiV1Module(), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(directory, "plugin.wasm")
	if err := os.Symlink(target, linked); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if _, err := ProbeFile(context.Background(), linked, "linked-plugin", DefaultSandboxOptions()); err == nil {
		t.Fatal("accepted symlink plugin entry")
	}
}

func TestValidateManifestRejectsImplicitNetwork(t *testing.T) {
	t.Parallel()
	manifest := Manifest{
		Schema: 1, Slug: "network-plugin", Name: "Network", Version: "1.0.0",
		Runtime: "wasm/core-v1", Entry: "plugin.wasm", OutboundHosts: []string{"example.com"},
	}
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("expected outbound host without capability to be rejected")
	}
}

func TestValidateManifestRejectsUnsafeDeclarations(t *testing.T) {
	t.Parallel()
	base := Manifest{
		Schema: 1, Slug: "safe-plugin", Name: "Safe", Version: "1.0.0",
		Runtime: "wasm/core-v1", Entry: "plugin.wasm",
	}
	tests := map[string]func(*Manifest){
		"non-canonical entry": func(manifest *Manifest) { manifest.Entry = "./plugin.wasm" },
		"backslash entry":     func(manifest *Manifest) { manifest.Entry = `dir\\plugin.wasm` },
		"duplicate host": func(manifest *Manifest) {
			manifest.Capabilities = []string{"http.outbound"}
			manifest.OutboundHosts = []string{"example.com", "example.com"}
		},
		"unsafe host": func(manifest *Manifest) {
			manifest.Capabilities = []string{"http.outbound"}
			manifest.OutboundHosts = []string{"Example.com"}
		},
		"invalid hook": func(manifest *Manifest) { manifest.Hooks = map[string][]string{"Render.Context": {"render_context"}} },
		"invalid admin path": func(manifest *Manifest) {
			manifest.Admin = &AdminContribution{Navigation: []AdminNav{{Label: "Unsafe", Path: "/extensions/../escape"}}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := base
			mutate(&manifest)
			if err := ValidateManifest(manifest); err == nil {
				t.Fatal("accepted unsafe plugin manifest")
			}
		})
	}
}

func TestProbeFileRejectsInvalidRuntimeOptions(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "plugin.wasm")
	if err := os.WriteFile(path, abiV1Module(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeFile(nil, path, "test-plugin", DefaultSandboxOptions()); err == nil {
		t.Fatal("accepted nil probe context")
	}
	if _, err := ProbeFile(context.Background(), path, "test-plugin", SandboxOptions{MemoryLimitMiB: 4097}); err == nil {
		t.Fatal("accepted excessive plugin memory limit")
	}
}

func TestInstallDefaultsDisabledAndCanEnable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "plugin.zip")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveWriter := zip.NewWriter(archiveFile)
	manifest := Manifest{
		Schema: 1, Slug: "hello-plugin", Name: "Hello", Version: "1.0.0",
		Runtime: "wasm/core-v1", Entry: "plugin.wasm",
	}
	manifestEntry, err := archiveWriter.Create("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(manifestEntry).Encode(manifest); err != nil {
		t.Fatal(err)
	}
	moduleEntry, err := archiveWriter.Create("plugin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := moduleEntry.Write(abiV1Module()); err != nil {
		t.Fatal(err)
	}
	if err := archiveWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(root)
	if _, err := manager.InstallPackage(archivePath); err != nil {
		t.Fatalf("install: %v", err)
	}
	items, err := manager.List()
	if err != nil || len(items) != 1 || items[0].Enabled {
		t.Fatalf("installed state = %#v, error = %v", items, err)
	}
	if err := manager.SetEnabled("hello-plugin", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	items, err = manager.List()
	if err != nil || len(items) != 1 || !items[0].Enabled {
		t.Fatalf("enabled state = %#v, error = %v", items, err)
	}
}

func TestInstallInvalidPluginLeavesNoInstallArtifacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "invalid-plugin.zip")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveWriter := zip.NewWriter(archiveFile)
	manifest := Manifest{
		Schema: 1, Slug: "broken-plugin", Name: "Broken", Version: "1.0.0",
		Runtime: "wasm/core-v1", Entry: "plugin.wasm",
	}
	manifestEntry, err := archiveWriter.Create("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(manifestEntry).Encode(manifest); err != nil {
		t.Fatal(err)
	}
	moduleEntry, err := archiveWriter.Create("plugin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := moduleEntry.Write([]byte("not wasm")); err != nil {
		t.Fatal(err)
	}
	if err := archiveWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := NewManager(root).InstallPackage(archivePath); err == nil {
		t.Fatal("accepted invalid plugin module")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed plugin installation left artifacts: %v", entries)
	}
}

func TestInstallRejectsLinkedPluginRoot(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	root := filepath.Join(t.TempDir(), "plugins")
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if _, err := NewManager(root).InstallPackage(filepath.Join(t.TempDir(), "missing.zip")); err == nil || !strings.Contains(err.Error(), "plugin root") {
		t.Fatalf("linked plugin root error = %v", err)
	}
}

func TestInstallRejectsLaterUnsafePluginEntryBeforeCreatingTemporaryDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "unsafe-plugin.zip")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveWriter := zip.NewWriter(archiveFile)
	manifest := Manifest{
		Schema: 1, Slug: "unsafe-plugin", Name: "Unsafe", Version: "1.0.0",
		Runtime: "wasm/core-v1", Entry: "plugin.wasm",
	}
	manifestEntry, err := archiveWriter.Create("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(manifestEntry).Encode(manifest); err != nil {
		t.Fatal(err)
	}
	moduleEntry, err := archiveWriter.Create("plugin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := moduleEntry.Write(abiV1Module()); err != nil {
		t.Fatal(err)
	}
	unsafeEntry, err := archiveWriter.Create("../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unsafeEntry.Write([]byte("bad")); err != nil {
		t.Fatal(err)
	}
	if err := archiveWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := NewManager(root).InstallPackage(archivePath); err == nil {
		t.Fatal("accepted plugin archive with unsafe later entry")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("preflight failure left plugin artifacts: %v", entries)
	}
}

func TestInstalledPluginDirectoryMustMatchManifestSlug(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "first-plugin")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Schema: 1, Slug: "second-plugin", Name: "Second", Version: "1.0.0",
		Runtime: "wasm/core-v1", Entry: "plugin.wasm",
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "plugin.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "plugin.wasm"), abiV1Module(), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root)
	items, err := manager.List()
	if err != nil || len(items) != 1 || !strings.Contains(items[0].Error, "does not match") {
		t.Fatalf("mismatched plugin listing = %#v, %v", items, err)
	}
	if err := manager.SetEnabled("first-plugin", true); err == nil {
		t.Fatal("enabled plugin whose manifest slug did not match its directory")
	}
}

func TestInstalledPluginRejectsSymlinkEntry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "linked-plugin")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Schema: 1, Slug: "linked-plugin", Name: "Linked", Version: "1.0.0",
		Runtime: "wasm/core-v1", Entry: "plugin.wasm",
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "plugin.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.wasm")
	if err := os.WriteFile(target, abiV1Module(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "plugin.wasm")); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	manager := NewManager(root)
	items, err := manager.List()
	if err != nil || len(items) != 1 || !strings.Contains(items[0].Error, "regular file") {
		t.Fatalf("symlink plugin listing = %#v, %v", items, err)
	}
	if err := manager.SetEnabled("linked-plugin", true); err == nil {
		t.Fatal("enabled plugin with symlink entry")
	}
}

func TestPluginListRejectsLinkedRoot(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	root := filepath.Join(t.TempDir(), "plugins")
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if _, err := NewManager(root).List(); err == nil || !strings.Contains(err.Error(), "plugin root") {
		t.Fatalf("linked plugin root listing error = %v", err)
	}
}

func TestPluginDisabledMarkerMustBeRegularAndCannotFollowLink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "marker-plugin")
	writeInstalledPlugin(t, directory, "marker-plugin")
	outside := filepath.Join(t.TempDir(), "outside-marker")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, ".disabled")
	if err := os.Symlink(outside, marker); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	manager := NewManager(root)
	items, err := manager.List()
	if err != nil || len(items) != 1 || items[0].Enabled || !strings.Contains(items[0].Error, "disabled marker") {
		t.Fatalf("linked disabled marker listing = %#v, %v", items, err)
	}
	if err := manager.SetEnabled("marker-plugin", true); err == nil {
		t.Fatal("enabled plugin with linked disabled marker")
	}
	if err := manager.SetEnabled("marker-plugin", false); err == nil {
		t.Fatal("overwrote linked disabled marker")
	}
	payload, err := os.ReadFile(outside)
	if err != nil || string(payload) != "outside" {
		t.Fatalf("linked marker target changed: %q, %v", payload, err)
	}
}

func TestInstallRejectsLinkedPluginTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manifest := Manifest{
		Schema: 1, Slug: "linked-target", Name: "Linked target", Version: "1.0.0",
		Runtime: "wasm/core-v1", Entry: "plugin.wasm",
	}
	archivePath := createPluginArchive(t, manifest, abiV1Module())
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, manifest.Slug)); err != nil {
		t.Skipf("creating symlink is unavailable: %v", err)
	}
	if _, err := NewManager(root).InstallPackage(archivePath); err == nil {
		t.Fatal("installed plugin through linked target")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != manifest.Slug {
		t.Fatalf("linked target installation artifacts = %v", entries)
	}
}

func writeInstalledPlugin(t *testing.T, directory, slug string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Schema: 1, Slug: slug, Name: "Installed", Version: "1.0.0",
		Runtime: "wasm/core-v1", Entry: "plugin.wasm",
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "plugin.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "plugin.wasm"), abiV1Module(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func createPluginArchive(t *testing.T, manifest Manifest, module []byte) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "plugin.zip")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveWriter := zip.NewWriter(archiveFile)
	manifestEntry, err := archiveWriter.Create("plugin.json")
	if err != nil {
		archiveFile.Close()
		t.Fatal(err)
	}
	if err := json.NewEncoder(manifestEntry).Encode(manifest); err != nil {
		archiveWriter.Close()
		archiveFile.Close()
		t.Fatal(err)
	}
	moduleEntry, err := archiveWriter.Create(manifest.Entry)
	if err != nil {
		archiveWriter.Close()
		archiveFile.Close()
		t.Fatal(err)
	}
	if _, err := moduleEntry.Write(module); err != nil {
		archiveWriter.Close()
		archiveFile.Close()
		t.Fatal(err)
	}
	if err := archiveWriter.Close(); err != nil {
		archiveFile.Close()
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func abiV1Module() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7e,
		0x03, 0x02, 0x01, 0x00,
		0x07, 0x12, 0x01, 0x0e,
		'n', 'p', '_', 'a', 'p', 'i', '_', 'v', 'e', 'r', 's', 'i', 'o', 'n',
		0x00, 0x00,
		0x0a, 0x06, 0x01, 0x04, 0x00, 0x42, 0x01, 0x0b,
	}
}
