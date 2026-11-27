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

func TestSigningRejectsFieldAliases(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "private.key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(private)), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := update.Manifest{Schema: 2, Product: update.CoreProduct, Sequence: 1, Expires: time.Now().Add(time.Hour), Version: "0.2.0", DataCompatible: true, Artifacts: []update.Artifact{{OS: "linux", Arch: "amd64", Path: "pkg.zip", Size: 10, SHA256: strings.Repeat("a", 64)}}}
	base, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, from, to string }{
		{"offline flag override", `"data_compatible":true`, `"data_compatible":false,"DATA_COMPATIBLE":true`},
		{"nested size override", `"size":10`, `"size":0,"SIZE":10`},
		{"Unicode sequence alias", `"sequence":1`, `"sequence":0,"ſequence":1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := bytes.Replace(base, []byte(test.from), []byte(test.to), 1)
			if bytes.Equal(base, body) {
				t.Fatal("fixture did not inject alias")
			}
			filename := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(filename, body, 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := sign(keyPath, filename, &out); err == nil || out.Len() != 0 {
				t.Errorf("ambiguous publication signed: error=%v, output bytes=%d", err, out.Len())
			}
			if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(after, body) {
				t.Errorf("source manifest changed: %v", err)
			}
		})
	}
}
