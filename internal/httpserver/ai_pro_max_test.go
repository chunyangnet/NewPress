package httpserver

import (
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/plugin"
)

func TestAIProMaxConsoleRequiresInstallAndRendersAvailableFeatures(t *testing.T) {
	s, handler, _, cookie, _ := newCommentHTTPTestServer(t)
	missing := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/plugins/ai-pro-max/", "", "", "8.8.8.8:80")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("uninstalled plugin dashboard: %d %s", missing.Code, missing.Body.String())
	}
	if err := s.plugins.InstallBundled(plugin.AIProMax); err != nil {
		t.Fatal(err)
	}
	if err := s.plugins.SetEnabled(plugin.AIProMax, true); err != nil {
		t.Fatal(err)
	}
	_, err := s.aiSettings.Save(aieditor.Settings{Revision: s.aiSettings.Snapshot().Revision, DefaultProvider: "writer", Providers: []aieditor.Provider{{ID: "writer", Name: "文本", BaseURL: "https://writer.example.test/v1", APIKey: "fixture-only", Models: []string{"model"}, DefaultModel: "model"}}})
	if err != nil {
		t.Fatal(err)
	}
	page := serveCommentRequest(handler, cookie, http.MethodGet, "https://example.test/admin/plugins/ai-pro-max/", "", "", "8.8.8.8:80")
	if page.Code != http.StatusOK || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("dashboard: %d %s", page.Code, page.Body.String())
	}
	for _, expected := range []string{"AI Pro Max", "AI 写作与草稿修订", "摘要与 SEO 元数据", "模型用量与费用", "AI 评论回复草稿", "自动评论审核与角色对话", "定时写作队列", "PostMeta 自动化触发", "周期选题与写作", "/admin/ai/"} {
		if !strings.Contains(page.Body.String(), expected) {
			t.Errorf("dashboard missing %q", expected)
		}
	}
}
