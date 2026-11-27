package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/update"
)

func TestSigningRequiresLosslessUnicode(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "private.key")
	keyBytes := []byte(base64.StdEncoding.EncodeToString(private))
	if err := os.WriteFile(keyPath, keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := update.Manifest{Schema: 2, Product: update.CoreProduct, Sequence: 1, Expires: time.Now().Add(time.Hour), Version: "0.2.0", Notes: "unicode-fixture", DataCompatible: true, Artifacts: []update.Artifact{{OS: "linux", Arch: "amd64", Path: "pkg.zip", Size: 10, SHA256: strings.Repeat("a", 64)}}}
	base, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, notes string
		valid       bool
	}{
		{"invalid UTF8", "\"release-\xff\"", false},
		{"high surrogate", `"release-\ud800"`, false},
		{"low surrogate", `"release-\udfff"`, false},
		{"Chinese and paired emoji", `"发布\ud83d\ude00"`, true},
		{"literal surrogate spelling", `"\\ud800"`, true},
		{"intentional replacement character", `"�\ufffd"`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := bytes.Replace(base, []byte(`"unicode-fixture"`), []byte(test.notes), 1)
			if bytes.Equal(body, base) {
				t.Fatal("fixture did not change notes")
			}
			filename := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(filename, body, 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := sign(keyPath, filename, &out)
			if !test.valid {
				if err == nil || out.Len() != 0 {
					t.Errorf("lossy release signed: error=%v, output bytes=%d", err, out.Len())
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var signed update.SignedManifest
				if err := json.Unmarshal(out.Bytes(), &signed); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(signed.Payload, body) || !ed25519.Verify(public, body, signed.Signature) {
					t.Error("signer changed the reviewed payload or signed different bytes")
				}
			}
			for path, want := range map[string][]byte{filename: body, keyPath: keyBytes} {
				if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, want) {
					t.Errorf("signer modified source file: %v", err)
				}
			}
		})
	}
}
