package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCoreReleaseRejectsSignedFieldAliases(t *testing.T) {
	key := fixtureKey(t)
	manifest := Manifest{Version: "0.2.0", DataCompatible: true, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "pkg.zip", Size: 10, SHA256: strings.Repeat("a", 64)}}}
	var signed SignedManifest
	if err := json.Unmarshal(signFixture(t, key, manifest, 1), &signed); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, from, to string }{
		{"offline flag override", `"data_compatible":true`, `"data_compatible":false,"DATA_COMPATIBLE":true`},
		{"nested size override", `"size":10`, `"size":0,"SIZE":10`},
		{"Unicode sequence alias", `"sequence":1`, `"sequence":0,"ſequence":1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := bytes.Replace(signed.Payload, []byte(test.from), []byte(test.to), 1)
			if bytes.Equal(payload, signed.Payload) {
				t.Fatal("fixture did not inject alias")
			}
			body, err := json.Marshal(SignedManifest{Payload: payload, Signature: ed25519.Sign(key, payload)})
			if err != nil {
				t.Fatal(err)
			}
			if got, err := decodeSigned(bytes.NewReader(body), key.Public().(ed25519.PublicKey)); err == nil {
				t.Errorf("authentic but ambiguous release accepted: %#v", got.Manifest)
			}
		})
	}
}

func TestUpdateRecordsRejectFieldAliasesBeforeMutation(t *testing.T) {
	for _, body := range []string{
		`{"phase":"staging","PHASE":"idle"}`,
		`{"phase":"idle","logs":[{"message":"first","MESSAGE":"second"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			state := State{Phase: "unchanged"}
			if err := readJSON(filename, &state); err == nil {
				t.Error("ambiguous update state accepted")
			}
			if state.Phase != "unchanged" || state.Logs != nil {
				t.Errorf("invalid state partially changed caller defaults: %#v", state)
			}
			if after, err := os.ReadFile(filename); err != nil || string(after) != body {
				t.Errorf("read changed file: %v", err)
			}
		})
	}
}
