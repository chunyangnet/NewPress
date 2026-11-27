package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

func TestPublisherKeyDefaultsOnlyForOfficialSource(t *testing.T) {
	t.Setenv("NEWPRESS_UPDATE_PUBLIC_KEY", "")
	key, err := publisherKey(officialSource())
	if err != nil || len(key) != ed25519.PublicKeySize {
		t.Fatalf("official embedded trust anchor unavailable: %v", err)
	}
	if _, err := publisherKey(testSource()); err == nil {
		t.Fatal("private source silently inherited the official publisher key")
	}
	m := managerFixture(t)
	view, err := m.Snapshot()
	if err != nil || !view.Source.Official || !view.TrustedPublisher {
		t.Fatalf("fresh installation did not trust the official publisher: %+v %v", view, err)
	}
}

func TestPublisherKeyExplicitOverrideAndInvalidValue(t *testing.T) {
	private := fixtureKey(t)
	want := private.Public().(ed25519.PublicKey)
	for _, source := range []Source{officialSource(), testSource()} {
		got, err := publisherKey(source)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("explicit publisher override ignored: %v", err)
		}
	}
	for _, value := range []string{"not-base64", base64.StdEncoding.EncodeToString([]byte("too short"))} {
		t.Setenv("NEWPRESS_UPDATE_PUBLIC_KEY", value)
		if _, err := publisherKey(officialSource()); err == nil {
			t.Fatal("invalid explicit publisher key fell back to official trust")
		}
	}
}

func TestEmbeddedOfficialKeyRejectsAnotherPublisher(t *testing.T) {
	private := fixtureKey(t)
	signed := signFixture(t, private, publicManifest([]byte("archive")), 1)
	t.Setenv("NEWPRESS_UPDATE_PUBLIC_KEY", "")
	key, err := publisherKey(officialSource())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSigned(bytes.NewReader(signed), key); err == nil {
		t.Fatal("unrelated publisher passed official signature verification")
	}
}
