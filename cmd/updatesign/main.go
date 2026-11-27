// Command updatesign signs an explicit core release manifest on the publishing host.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/update"
)

const maxSigningKeyFileBytes = 256

func main() {
	key := flag.String("key", "", "private Ed25519 key file")
	payload := flag.String("manifest", "", "reviewed release manifest file")
	flag.Parse()
	if err := sign(*key, *payload, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func sign(keyPath, manifestPath string, out io.Writer) error {
	f, _, err := packagefs.OpenRegularNoSymlink(keyPath)
	if err != nil {
		return errors.New("cannot open signing key")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxSigningKeyFileBytes+1))
	if err != nil {
		return errors.New("cannot read signing key")
	}
	if len(raw) > maxSigningKeyFileBytes {
		return fmt.Errorf("signing key file exceeds %d bytes", maxSigningKeyFileBytes)
	}
	key, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(raw)))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 private key")
	}
	derived := ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])
	if !bytes.Equal(derived, key) {
		return errors.New("inconsistent Ed25519 key")
	}
	mf, _, err := packagefs.OpenRegularNoSymlink(manifestPath)
	if err != nil {
		return err
	}
	defer mf.Close()
	payload, err := io.ReadAll(io.LimitReader(mf, (64<<10)+1))
	if err != nil {
		return err
	}
	m, err := packagefs.DecodeJSON[update.Manifest](bytes.NewReader(payload), 64<<10, "core manifest")
	if err != nil || m.Product != update.CoreProduct || m.Sequence < 1 || !m.Expires.After(time.Now()) {
		return errors.New("invalid or expired core manifest")
	}
	if err := m.ValidateContents(); err != nil {
		return fmt.Errorf("invalid core manifest: %w", err)
	}
	return json.NewEncoder(out).Encode(update.SignedManifest{Payload: payload, Signature: ed25519.Sign(derived, payload)})
}
