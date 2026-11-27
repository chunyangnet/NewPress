package httpserver

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/feedback"
	"newpress.local/newpress/internal/linkpage"
)

func TestFeatureCatalogIncludesBothPageTypes(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	index := "https://example.test/admin/features/"
	empty := serveContentAPI(handler, cookie, http.MethodGet, index, "", nil)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), "还没有功能页面") {
		t.Fatalf("empty feature catalog: %d %s", empty.Code, empty.Body.String())
	}
	// Identical slugs belong to different stores. Both must stay individually
	// addressable, including disabled pages that need configuration access.
	feedbackSettings := feedback.Defaults()
	feedbackSettings.Name, feedbackSettings.Enabled = "反馈 <研究>", false
	feedbackSettings.Description = "反馈页面的独立介绍"
	if _, err := s.feedback.Apply(t.Context(), "shared", feedbackActor(owner), feedback.Command{
		Action: "create", Key: "catalog-create-feedback", Settings: &feedbackSettings,
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	linkSettings := linkpage.Defaults()
	linkSettings.Name, linkSettings.Description = "友链 <设计>", "友链页面的独立介绍"
	if _, err := s.linkPages.Apply(t.Context(), "shared", linkpage.Actor{ID: owner.UserID, Admin: true}, linkpage.Command{
		Action: "create", Key: "catalog-create-links", ExpectedUserID: owner.UserID, Settings: &linkSettings,
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	response := serveContentAPI(handler, cookie, http.MethodGet, index, "", nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("feature catalog: %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, want := range []string{
		`class="feature-page-count">2`, `href="/admin/features/?edit=shared"`, `href="/admin/features/links/?edit=shared"`,
		`href="/feedback/shared/"`, `href="/links/shared/"`, `data-state="disabled"`, `data-state="enabled"`,
		"反馈 &lt;研究&gt;", "友链 &lt;设计&gt;", "反馈页面的独立介绍", "友链页面的独立介绍",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("feature catalog missing %q", want)
		}
	}
	if strings.Contains(body, "还没有功能页面") || strings.Contains(body, "反馈 <研究>") || strings.Contains(body, "友链 <设计>") {
		t.Error("feature catalog rendered an empty state or unescaped title")
	}
	if strings.Index(body, `class="feature-page-row" data-feature-kind="links"`) > strings.Index(body, `class="feature-page-row" data-feature-kind="feedback"`) {
		t.Error("open pages should precede disabled pages")
	}
	for _, path := range []string{index + "?edit=shared", "https://example.test/admin/features/links/?edit=shared"} {
		r := serveContentAPI(handler, cookie, http.MethodGet, path, "", nil)
		if r.Code != http.StatusOK {
			t.Fatalf("catalog destination %s: %d %s", path, r.Code, r.Body.String())
		}
	}
	board, _ := s.feedback.Get(t.Context(), "shared")
	links, _ := s.linkPages.Get(t.Context(), "shared")
	if board.Version != 1 || links.Version != 1 {
		t.Fatal("reading the catalog changed a page")
	}
}
