package theme

import (
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/presentation"
)

func TestNexoraEnterpriseJourney(t *testing.T) {
	m := NewManager(filepath.Join("..", "..", "themes"), "nexora", WithSettingsDir(t.TempDir()))
	if err := m.LoadExplicit("nexora"); err != nil {
		t.Fatal(err)
	}
	p := presentation.Page{Kind: "home", Settings: m.SettingsSnapshot().Values, Modules: map[string]bool{}}
	render := func() string {
		t.Helper()
		var out RenderBuffer
		if err := m.Render(&out, p.Kind, p); err != nil {
			t.Fatal(err)
		}
		return string(out.Bytes())
	}
	for _, key := range []string{"show_cases", "show_resources", "show_process"} {
		p.Settings[key] = true
	}
	// Empty real-world content must not produce fabricated projects or downloads.
	html := render()
	for _, marker := range []string{`id="cases"`, `id="resources"`, `进入用户中心`} {
		if strings.Contains(html, marker) {
			t.Fatalf("unexpected empty-state output %s", marker)
		}
	}
	p.Settings["case_1_title"] = "真实项目 <script>alert(1)</script>"
	p.Settings["case_1_url"] = "/pages/customer-story/"
	p.Settings["resource_1_title"] = "产品白皮书"
	p.Settings["resource_1_url"] = "/uploads/whitepaper.pdf"
	p.Settings["job_1_title"] = "研发工程师"
	p.Settings["job_1_text"] = "职位要求与工作职责"
	p.Settings["job_1_url"] = "/pages/engineering/"
	p.Settings["contact_email"] = "business@example.test"
	html = render()
	for _, marker := range []string{`id="cases"`, `id="resources"`, `id="process"`, `href="/pages/customer-story/"`, `href="/uploads/whitepaper.pdf"`, `href="/pages/engineering/"`, `href="mailto:business@example.test"`, `&lt;script&gt;`} {
		if !strings.Contains(html, marker) {
			t.Fatalf("missing enterprise journey output %s", marker)
		}
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatal("case title was not escaped")
	}
	// Removed modules must disappear from default navigation and configured CTAs.
	p.Settings["show_cases"] = false
	p.Settings["hero_link"] = "/#cases"
	p.Settings["contact_action_url"] = "/#cases"
	if html = render(); strings.Contains(html, `href="/#cases"`) || strings.Contains(html, `id="cases"`) {
		t.Fatal("disabled cases left a broken anchor")
	}
	p.Settings["contact_email"] = ""
	p.Settings["contact_phone"] = "+861012345678"
	if !strings.Contains(render(), `href="tel:&#43;861012345678"`) {
		t.Fatal("missing telephone fallback")
	}
	p.Settings["contact_phone"] = ""
	p.Modules["feedback"] = true
	if !strings.Contains(render(), `href="/feedback/"`) {
		t.Fatal("missing feedback fallback")
	}
	p.Kind = "page"
	p.Settings["page_contact_cta"] = true
	if !strings.Contains(render(), "enterprise-page-contact") {
		t.Fatal("missing content-to-inquiry conversion")
	}
	p.IsPreview = true
	if strings.Contains(render(), "enterprise-page-contact") {
		t.Fatal("preview contains conversion panel")
	}
}

func TestNexoraEnterpriseSettingsValidation(t *testing.T) {
	m := NewManager(filepath.Join("..", "..", "themes"), "nexora", WithSettingsDir(t.TempDir()))
	if err := m.LoadExplicit("nexora"); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"case", "resource", "job"} {
		for _, n := range []string{"1", "2", "3"} {
			key := prefix + "_" + n + "_url"
			for _, value := range []string{"javascript:alert(1)", "//example.test", "/path with space", "#cases"} {
				values := m.SettingsSnapshot().Values
				values[key] = value
				if _, _, _, fields := m.PreviewSettings(values); fields[key] == "" {
					t.Fatalf("%s accepted %q", key, value)
				}
			}
		}
	}
}
