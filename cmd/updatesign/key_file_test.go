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

func TestSigningKeyFileMustBeCompleteAndBounded(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(private)
	const limit = 256
	padded := encoded + strings.Repeat(" ", limit-len(encoded))
	inconsistent := bytes.Clone(private)
	inconsistent[len(inconsistent)-1] ^= 1
	payload, err := json.Marshal(update.Manifest{Schema: 2, Product: update.CoreProduct, Sequence: 1, Version: "0.2.0", Expires: time.Now().UTC().Add(time.Hour), Notes: "Reviewed withdrawal fixture"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"plain key", encoded, true},
		{"LF", encoded + "\n", true},
		{"CRLF", encoded + "\r\n", true},
		{"surrounding whitespace", " \t" + encoded + "\r\n", true},
		{"exact file limit", padded, true},
		{"oversized whitespace", padded + " ", false},
		{"hidden suffix", padded + "hidden-suffix", false},
		{"second key after limit", padded + encoded, false},
		{"malformed base64", "invalid-key", false},
		{"seed instead of private key", base64.StdEncoding.EncodeToString(private[:ed25519.SeedSize]), false},
		{"inconsistent private key", base64.StdEncoding.EncodeToString(inconsistent), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			keyPath := filepath.Join(root, "private.key")
			manifestPath := filepath.Join(root, "manifest.json")
			if err := os.WriteFile(keyPath, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err := sign(keyPath, manifestPath, &output)
			if test.valid {
				if err != nil {
					t.Fatalf("valid signing key file rejected: %v", err)
				}
				var signed update.SignedManifest
				if err := json.Unmarshal(output.Bytes(), &signed); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(signed.Payload, payload) || !ed25519.Verify(public, signed.Payload, signed.Signature) {
					t.Error("signer changed the reviewed manifest or produced an invalid signature")
				}
			} else {
				if err == nil {
					t.Error("invalid or oversized key file was accepted")
				}
				if output.Len() != 0 {
					t.Error("invalid key file produced a signed publication")
				}
				if err != nil && (strings.Contains(err.Error(), encoded) || strings.Contains(err.Error(), keyPath)) {
					t.Error("signing error disclosed the private key or path")
				}
			}
			if after, err := os.ReadFile(keyPath); err != nil || string(after) != test.body {
				t.Errorf("signing changed the input key file: %v", err)
			}
			if after, err := os.ReadFile(manifestPath); err != nil || !bytes.Equal(after, payload) {
				t.Errorf("signing changed the reviewed manifest: %v", err)
			}
		})
	}
}
