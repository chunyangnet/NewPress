package theme

import (
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/presentation"
)

func TestNexoraServicePages(t *testing.T) {
	m := NewManager(filepath.Join("..", "..", "themes"), "nexora", WithSettingsDir(t.TempDir()))
	if err := m.LoadExplicit("nexora"); err != nil {
		t.Fatal(err)
	}
	pages := map[string]string{
		"account": "data-account-room", "account-access": "data-account-access", "billing": "data-billing-room",
		"shop": "data-shop", "feedback": "data-feedback-page", "questions": "qa-page", "circles": "circle",
		"links": "links-", "flash": "flash-page", "flash-single": "flash-", "flash-submit": "flash-",
		"notification-unsubscribe": "/account/notifications/unsubscribe/", "community": "交流与服务",
	}
	for kind, marker := range pages {
		t.Run(kind, func(t *testing.T) {
			p := presentation.Fixtures()["components"]
			p.Settings = m.SettingsSnapshot().Values
			p.Kind = kind
			var out RenderBuffer
			if err := m.Render(&out, kind, p); err != nil {
				t.Fatal(err)
			}
			html := string(out.Bytes())
			for _, want := range []string{`data-theme="nexora"`, `nexora portal-site`, marker, `data-account-menu`, `data-account-name`, `data-account-avatar`, `data-account-admin`, `data-menu-unread`, `id="account-login-modal"`, `/core-assets/js/newpress-client.js`} {
				if !strings.Contains(html, want) {
					t.Errorf("missing service contract %s", want)
				}
			}
			if strings.Contains(html, "#ZgotmplZ") || strings.Contains(html, "/themes/docshub/") {
				t.Fatal("invalid URL or cross-theme dependency")
			}
		})
	}
}

func TestNexoraFeedbackWorkflowAndAnnouncements(t *testing.T) {
	m := NewManager(filepath.Join("..", "..", "themes"), "nexora", WithSettingsDir(t.TempDir()))
	if err := m.LoadExplicit("nexora"); err != nil {
		t.Fatal(err)
	}
	p := presentation.Fixtures()["components"]
	p.Settings = m.SettingsSnapshot().Values
	p.Kind = "feedback"
	p.Feedback.CanSubmit, p.Feedback.Manage, p.Feedback.SignedIn = true, true, true
	p.Feedback.Item.CanComment, p.Feedback.Item.CanEdit = true, true
	p.Feedback.Settings.OfficialReplies = true
	var out RenderBuffer
	if err := m.Render(&out, p.Kind, p); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-feedback-form="edit"`, `data-feedback-form="reply"`, `data-feedback-form="status"`, `data-official="true"`, `data-feedback-conflict`} {
		if !strings.Contains(string(out.Bytes()), want) {
			t.Errorf("missing feedback operation %s", want)
		}
	}
	p.Kind, p.BodyClass = "page", "is-page is-announcements"
	out.Reset()
	if err := m.Render(&out, p.Kind, p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out.Bytes()), "announcement-document") {
		t.Fatal("announcement detail fell through to generic page")
	}
}
