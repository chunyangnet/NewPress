package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestSignedReleaseRequiresLosslessUnicode(t *testing.T) {
	key := fixtureKey(t)
	manifest := Manifest{Version: "0.2.0", Notes: "unicode-fixture", DataCompatible: true, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "pkg.zip", Size: 10, SHA256: strings.Repeat("a", 64)}}}
	var signed SignedManifest
	if err := json.Unmarshal(signFixture(t, key, manifest, 1), &signed); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, notes, want string
		valid             bool
	}{
		{"invalid UTF8", "\"release-\xff\"", "", false},
		{"high surrogate", `"release-\ud800"`, "", false},
		{"low surrogate", `"release-\udfff"`, "", false},
		{"Chinese and paired emoji", `"发布\ud83d\ude00"`, "发布😀", true},
		{"literal surrogate spelling", `"\\ud800"`, `\ud800`, true},
		{"intentional replacement character", `"�\ufffd"`, "��", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := bytes.Replace(signed.Payload, []byte(`"unicode-fixture"`), []byte(test.notes), 1)
			if bytes.Equal(payload, signed.Payload) {
				t.Fatal("fixture did not change notes")
			}
			envelope := SignedManifest{Payload: payload, Signature: ed25519.Sign(key, payload)}
			public := key.Public().(ed25519.PublicKey)
			if !ed25519.Verify(public, payload, envelope.Signature) {
				t.Fatal("fixture signature is invalid")
			}
			body, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeSigned(bytes.NewReader(body), public)
			if !test.valid {
				if err == nil {
					t.Errorf("authentic release accepted with lossy notes: %q", got.Manifest.Notes)
				}
			} else if err != nil || got.Manifest.Notes != test.want || got.PayloadHash != digest(payload) {
				t.Fatalf("valid signed release changed: %#v, %v", got, err)
			}
		})
	}
}
