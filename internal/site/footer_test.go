package site

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomFooterSanitizesPersistsAndClears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site-settings.json")
	m, err := NewManager(path, DefaultSettings("Footer", "Footer"))
	if err != nil {
		t.Fatal(err)
	}
	initial := m.Snapshot()
	candidate := initial.Settings
	candidate.Integrations = &IntegrationsSettings{
		FooterCopyright: " © 2026 我们的社区 ", FooterHideCredit: true,
		FooterHTML: `<p onclick="alert(1)">一起分享 <a href="/privacy/">隐私政策</a></p><script>alert(1)</script><a href="javascript:alert(1)">危险链接</a>`,
	}
	saved, fields, err := m.Save(initial.Revision, candidate)
	if err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	got := saved.Settings.IntegrationValues()
	if got.FooterCopyright != "© 2026 我们的社区" || !got.FooterHideCredit || !strings.Contains(got.FooterHTML, `href="/privacy/"`) {
		t.Fatalf("footer values = %#v", got)
	}
	for _, unsafe := range []string{"<script", "onclick", "javascript:"} {
		if strings.Contains(got.FooterHTML, unsafe) {
			t.Fatalf("unsafe footer content survived: %s", got.FooterHTML)
		}
	}
	loaded, err := NewManager(path, DefaultSettings("", ""))
	if err != nil || loaded.Snapshot().Settings.IntegrationValues() != got {
		t.Fatal("footer did not round-trip", err)
	}
	clear := saved.Settings
	clear.Integrations = &IntegrationsSettings{}
	cleared, fields, err := loaded.Save(saved.Revision, clear)
	if err != nil || len(fields) != 0 || cleared.Revision != initial.Revision {
		t.Fatal("clear changed default revision", fields, err)
	}
}

func TestCustomFooterRejectsOversizedAndInvalidText(t *testing.T) {
	for _, value := range []string{strings.Repeat("文", 3000), strings.Repeat("&", 8192), "hello\x00world", string([]byte{0xff})} {
		s := DefaultSettings("", "")
		s.Integrations = &IntegrationsSettings{FooterHTML: value}
		if _, fields := Validate(s); fields["footer_html"] == "" {
			t.Fatal("invalid footer accepted")
		}
	}
}
