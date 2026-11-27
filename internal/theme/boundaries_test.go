package theme

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMissingTemplateNeverRenders404(t *testing.T) {
	m := newTimezoneTestManager(t, `404 sentinel`)
	var output bytes.Buffer
	if err := m.Render(&output, "missing", nil); !errors.Is(err, ErrTemplateMissing) || output.Len() != 0 {
		t.Fatalf("missing template = %v, body %q", err, output.String())
	}
}

func TestAssetsChangeIdentityOnReloadAndRestoreOnRollback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "independent")
	for name, data := range map[string]string{
		"theme.json":          `{"schema":1,"slug":"independent","name":"Independent","version":"1.0.0","engine":"go-template/v1","view":"public/v1","templates":{"home":"page.html","single":"page.html"}}`,
		"templates/base.html": `{{define "base"}}{{template "content" .}}{{end}}`,
		"templates/page.html": `{{define "content"}}<link href="{{asset "css/main.css"}}">{{end}}`,
		"assets/css/main.css": "old content",
	} {
		file := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(data), 0640); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(root, "independent")
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	before := m.AssetURL("css/main.css")
	for _, item := range []struct {
		data     string
		restored bool
	}{{"changed content", false}, {"old content", true}} {
		if err := os.WriteFile(filepath.Join(dir, "assets/css/main.css"), []byte(item.data), 0640); err != nil {
			t.Fatal(err)
		}
		if err := m.Load(); err != nil {
			t.Fatal(err)
		}
		if (m.AssetURL("css/main.css") == before) != item.restored {
			t.Fatal("asset identity does not follow bytes")
		}
	}
}

func TestCapabilitiesRequireExplicitDeclaration(t *testing.T) {
	for _, item := range []struct {
		manifest Manifest
		want     bool
	}{
		{Manifest{Slug: "independent"}, false},
		{Manifest{Slug: "chunnel"}, false},
		{Manifest{Slug: "chunnel", View: PublicViewV1}, false},
		{Manifest{Slug: "independent", View: PublicViewV1, Capabilities: []string{HomeLayoutV1}}, true},
	} {
		if got := item.manifest.Supports(HomeLayoutV1); got != item.want {
			t.Fatalf("capability %v = %v", item.manifest, got)
		}
	}
}

func TestThemeOTACanRemoveRetiredAdminEntries(t *testing.T) {
	m, manifest := themeOTAFixture(t)
	manifest.Templates["admin"] = "page.html"
	manifest.Templates["admin-login"] = "page.html"
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.root, manifest.Slug, "theme.json"), raw, 0640); err != nil {
		t.Fatal(err)
	}
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	target, err := m.CaptureUpdate(manifest.Slug, "", "")
	if err != nil {
		t.Fatal(err)
	}
	delete(manifest.Templates, "admin")
	delete(manifest.Templates, "admin-login")
	manifest.Version = "1.1.0"
	release, archive := themeOTAArchive(t, manifest, otaSettings, `{{define "content"}}public-only{{end}}`)
	if err := m.UpdatePackage(t.Context(), archive, release, target, nil); err != nil {
		t.Fatal(err)
	}
	if m.HasTemplate("admin") || !m.HasTemplate("home") {
		t.Fatal("admin entry removal did not preserve public theme")
	}
}

func TestThemeViewMustBeExplicit(t *testing.T) {
	m := Manifest{Schema: 1, Slug: "sample", Name: "Sample", Version: "1.0.0", Engine: "go-template/v1", Templates: map[string]string{"home": "home.html", "single": "single.html"}}
	if err := ValidateManifest(m); err == nil {
		t.Fatal("unversioned theme view accepted")
	}
	m.View = PublicViewV1
	if err := ValidateManifest(m); err != nil {
		t.Fatal(err)
	}
	m.View = ChunnelViewV1
	if err := ValidateManifest(m); err == nil {
		t.Fatal("Chunnel-specific view exposed to another theme")
	}
	m.Slug = "chunnel"
	if err := ValidateManifest(m); err != nil {
		t.Fatal(err)
	}
}
