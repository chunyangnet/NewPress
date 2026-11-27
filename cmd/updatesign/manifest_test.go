package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/update"
)

func TestSigningValidatesAllPlatformManifestContents(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "private.key")
	keyBody := []byte(base64.StdEncoding.EncodeToString(private))
	if err := os.WriteFile(keyPath, keyBody, 0o600); err != nil {
		t.Fatal(err)
	}
	foreignOS := "linux"
	if runtime.GOOS == foreignOS {
		foreignOS = "windows"
	}
	packages := func(count int) func(*update.Manifest) {
		return func(m *update.Manifest) {
			artifact := m.Artifacts[0]
			m.Artifacts = nil
			for index := 0; index < count; index++ {
				artifact.Arch = fmt.Sprintf("arch%d", index)
				m.Artifacts = append(m.Artifacts, artifact)
			}
		}
	}
	for _, test := range []struct {
		name  string
		edit  func(*update.Manifest)
		valid bool
	}{
		{"foreign platform release", func(*update.Manifest) {}, true},
		{"withdrawal", func(m *update.Manifest) { m.DataCompatible = false; m.Artifacts = nil }, true},
		{"offline release", func(m *update.Manifest) { m.DataCompatible = false }, true},
		{"maximum notes", func(m *update.Manifest) { m.Notes = strings.Repeat("n", 16000) }, true},
		{"maximum package", func(m *update.Manifest) { m.Artifacts[0].Size = update.MaxPackageBytes }, true},
		{"maximum platforms", packages(16), true},
		{"oversized notes", func(m *update.Manifest) { m.Notes = strings.Repeat("n", 16001) }, false},
		{"multibyte notes limit", func(m *update.Manifest) { m.Notes = strings.Repeat("界", 5334) }, false},
		{"missing installable packages", func(m *update.Manifest) { m.Artifacts = nil }, false},
		{"too many platforms", packages(17), false},
		{"duplicate platform", func(m *update.Manifest) { m.Artifacts = append(m.Artifacts, m.Artifacts[0]) }, false},
		{"empty OS", func(m *update.Manifest) { m.Artifacts[0].OS = "" }, false},
		{"empty architecture", func(m *update.Manifest) { m.Artifacts[0].Arch = "" }, false},
		{"OS contains separator", func(m *update.Manifest) { m.Artifacts[0].OS = "linux/other" }, false},
		{"architecture contains separator", func(m *update.Manifest) { m.Artifacts[0].Arch = "arm/64" }, false},
		{"traversal path", func(m *update.Manifest) { m.Artifacts[0].Path = "../pkg.zip" }, false},
		{"noncanonical path", func(m *update.Manifest) { m.Artifacts[0].Path = "packages//pkg.zip" }, false},
		{"non ZIP path", func(m *update.Manifest) { m.Artifacts[0].Path = "pkg.tar" }, false},
		{"zero package size", func(m *update.Manifest) { m.Artifacts[0].Size = 0 }, false},
		{"negative package size", func(m *update.Manifest) { m.Artifacts[0].Size = -1 }, false},
		{"oversized package", func(m *update.Manifest) { m.Artifacts[0].Size = update.MaxPackageBytes + 1 }, false},
		{"short checksum", func(m *update.Manifest) { m.Artifacts[0].SHA256 = "abcd" }, false},
		{"nonhex checksum", func(m *update.Manifest) { m.Artifacts[0].SHA256 = strings.Repeat("g", 64) }, false},
		{"uppercase checksum", func(m *update.Manifest) { m.Artifacts[0].SHA256 = strings.Repeat("A", 64) }, false},
		{"malformed secondary package", func(m *update.Manifest) {
			bad := m.Artifacts[0]
			bad.Arch, bad.Path = "other", "../other.zip"
			m.Artifacts = append(m.Artifacts, bad)
		}, false},
		{"offline malformed package", func(m *update.Manifest) { m.DataCompatible = false; m.Artifacts[0].Size = 0 }, false},
		{"withdrawal oversized notes", func(m *update.Manifest) {
			m.DataCompatible, m.Artifacts, m.Notes = false, nil, strings.Repeat("n", 16001)
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := update.Manifest{Schema: 2, Product: update.CoreProduct, Sequence: 1, Version: "0.2.0", Expires: time.Now().UTC().Add(time.Hour), DataCompatible: true, Artifacts: []update.Artifact{{OS: foreignOS, Arch: "arm64", Path: "pkg.zip", Size: 10, SHA256: strings.Repeat("a", 64)}}}
			test.edit(&manifest)
			payload, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(filename, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err = sign(keyPath, filename, &output)
			if (err == nil) != test.valid {
				t.Errorf("manifest accepted=%v, want %v: %v", err == nil, test.valid, err)
			}
			if test.valid {
				var signed update.SignedManifest
				if err := json.Unmarshal(output.Bytes(), &signed); err != nil || !bytes.Equal(signed.Payload, payload) || !ed25519.Verify(public, signed.Payload, signed.Signature) {
					t.Errorf("valid publication was changed or signed incorrectly: %v", err)
				}
			} else if output.Len() != 0 {
				t.Error("invalid manifest produced a signed publication")
			}
			if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(after, payload) {
				t.Errorf("signing changed the source manifest: %v", err)
			}
			if after, err := os.ReadFile(keyPath); err != nil || !bytes.Equal(after, keyBody) {
				t.Errorf("signing changed the private key: %v", err)
			}
		})
	}
}
