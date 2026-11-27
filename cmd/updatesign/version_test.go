package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/update"
)

func TestSigningUsesCoreVersionContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "update", "testdata", "core_versions.json"))
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
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "private.key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(private)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			payload, err := json.Marshal(update.Manifest{Schema: 2, Product: update.CoreProduct, Sequence: 1, Version: test.Version, Expires: time.Now().UTC().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(filename, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err = sign(keyPath, filename, &output)
			if (err == nil) != test.Valid {
				t.Errorf("signing version %q: accepted=%v, want %v", test.Version, err == nil, test.Valid)
			}
			if !test.Valid {
				if output.Len() != 0 {
					t.Error("invalid core version produced a signed manifest")
				}
				return
			}
			var signed update.SignedManifest
			if err := json.Unmarshal(output.Bytes(), &signed); err != nil || !bytes.Equal(signed.Payload, payload) || !ed25519.Verify(public, signed.Payload, signed.Signature) {
				t.Errorf("valid version did not retain the reviewed payload and signature: %v", err)
			}
		})
	}
}
