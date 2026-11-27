package plugineffects

import (
	"errors"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/pluginjobs"
)

func TestEffectIdentitySurvivesLeaseButBindsRequest(t *testing.T) {
	title := "title"
	r := Request{Authorization: Authorization{Claim: pluginjobs.Job{ID: strings.Repeat("a", 64)}, ManifestDigest: strings.Repeat("b", 64), ModuleDigest: strings.Repeat("c", 64)},
		Slot: "summary", PostID: 1, Update: ContentUpdate{ExpectedVersion: 2, Patch: content.TextPatch{Title: &title}}}
	id, fingerprint, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	r.Authorization.Claim.LeaseToken = "different"
	r.Authorization.Claim.Version++
	r.Authorization.Claim.LeaseUntil = time.Now().Add(time.Hour)
	again, repeated, err := r.Identity()
	if err != nil || again != id || repeated != fingerprint {
		t.Fatal("lease changed stable operation", err)
	}
	r.PostID++
	again, changed, err := r.Identity()
	if err != nil || again != id || changed == fingerprint {
		t.Fatal("target not bound", err)
	}
	r.Slot = "new-summary"
	again, _, _ = r.Identity()
	if again == id {
		t.Fatal("slots collided")
	}
	r.Slot = "../secret"
	if _, _, err = r.Identity(); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid slot", err)
	}
}

func TestEffectDecodeRejectsAmbiguousWrites(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"version":1,"patch":null}`, `{"version":1,"patch":{"title":"a","excerpt":null}}`,
		`{"version":1,"patch":{"status":"publish"}}`, `{"version":1,"patch":{"title":"a","title":"b"}}`, `{"version":1,"actor_id":1,"patch":{"title":"a"}}`,
		`{"version":1,"patch":{"title":"\ud800"}}`} {
		if _, err := DecodeContentUpdate([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := DecodeContentUpdate([]byte(`{"version":1,"patch":{"excerpt":""}}`)); err != nil {
		t.Fatal(err)
	}
}
