package theme

import (
	"errors"
	"newpress.local/newpress/internal/extension"
	"testing"
)

func TestChildDependencyTokens(t *testing.T) {
	m, parent := themeOTAFixture(t)
	before, err := m.CaptureUpdate(parent.Slug, "", "")
	if err != nil {
		t.Fatal(err)
	}
	child := childFixture(t, m, parent)
	if _, err := m.CaptureUpdate(parent.Slug, parent.Version, before.state.Revision); !errors.Is(err, extension.ErrConflict) {
		t.Fatalf("new dependent did not invalidate parent token: %v", err)
	}
	childBefore, err := m.VersionState(child.Slug)
	if err != nil {
		t.Fatal(err)
	}
	target, err := m.CaptureUpdate(parent.Slug, "", "")
	if err != nil {
		t.Fatal(err)
	}
	parent.Version = "1.1.0"
	release, archive := themeOTAArchive(t, parent, otaSettings, `{{define "content"}}updated{{end}}`)
	if err := m.UpdatePackage(t.Context(), archive, release, target, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CaptureUpdate(child.Slug, child.Version, childBefore.Revision); !errors.Is(err, extension.ErrConflict) {
		t.Fatalf("parent update did not invalidate child token: %v", err)
	}
}
