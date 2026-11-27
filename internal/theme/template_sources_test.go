package theme

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompiledTemplateSourcesRemainIndependentAndReloadable(t *testing.T) {
	directory := t.TempDir()
	partials := filepath.Join(directory, "templates", "partials")
	if err := os.MkdirAll(partials, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name, source string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(directory, "templates", name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("base.html", `{{define "base"}}<main>{{template "shared" .}}|{{template "body" .}}</main>{{end}}`)
	write("partials/shared.html", `{{define "shared"}}original{{end}}`)
	write("home.html", `{{define "body"}}HOME {{.}}{{end}}`)
	write("list.html", `{{define "body"}}LIST {{.}}{{end}}`)
	manifest := Manifest{Templates: map[string]string{"home": "home.html", "list": "list.html", "404": "home.html"}}
	compiled, err := compileTemplates(directory, manifest)
	if err != nil {
		t.Fatal(err)
	}
	assertPages := func(compiled map[string]*template.Template, shared string) {
		t.Helper()
		for _, page := range []string{"home", "list", "404", "list", "home"} {
			var rendered bytes.Buffer
			if err := compiled[page].ExecuteTemplate(&rendered, "base", "<input>"); err != nil {
				t.Fatal(err)
			}
			body := "HOME"
			if page == "list" {
				body = "LIST"
			}
			want := "<main>" + shared + "|" + body + " &lt;input&gt;</main>"
			if rendered.String() != want {
				t.Fatalf("page %s inherited another template or lost escaping: %q", page, rendered.String())
			}
		}
	}
	assertPages(compiled, "original")
	write("partials/shared.html", `{{define "shared"}}reloaded{{end}}`)
	reloaded, err := compileTemplates(directory, manifest)
	if err != nil {
		t.Fatal(err)
	}
	assertPages(reloaded, "reloaded")
	assertPages(compiled, "original")
	write("partials/shared.html", `{{define "shared"}}{{missingFunction}}{{end}}`)
	if _, err := compileTemplates(directory, manifest); err == nil {
		t.Fatal("reload reused cached source instead of rejecting a broken template")
	}
	write("partials/shared.html", `{{define "shared"}}repaired{{end}}`)
	repaired, err := compileTemplates(directory, manifest)
	if err != nil {
		t.Fatal(err)
	}
	assertPages(repaired, "repaired")
}

func BenchmarkCompileSharedTemplateSources(b *testing.B) {
	directory := b.TempDir()
	partials := filepath.Join(directory, "templates", "partials")
	if err := os.MkdirAll(partials, 0700); err != nil {
		b.Fatal(err)
	}
	files := map[string]string{"base.html": `{{define "base"}}{{template "body" .}}{{end}}`}
	for i := range 12 {
		files[fmt.Sprintf("partials/shared-%02d.html", i)] = fmt.Sprintf(`{{define "shared%d"}}%s{{end}}`, i, strings.Repeat("shared template content ", 16))
	}
	manifest := Manifest{Templates: make(map[string]string)}
	for i := range 16 {
		name := fmt.Sprintf("page-%02d.html", i)
		manifest.Templates[fmt.Sprintf("page%d", i)] = name
		files[name] = fmt.Sprintf(`{{define "body"}}page %d{{template "shared0" .}}{{end}}`, i)
	}
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(directory, "templates", name), []byte(source), 0600); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := compileTemplates(directory, manifest); err != nil {
			b.Fatal(err)
		}
	}
}
