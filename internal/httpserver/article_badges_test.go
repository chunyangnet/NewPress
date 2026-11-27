package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestArticleBadgeEditorSavePublicRenderAndAutosave(t *testing.T) {
	s, handler, repo, cookie, principal := newAuthenticatedContentServer(t)
	p, err := repo.Create(t.Context(), content.PostInput{Title: "角标测试", Slug: "badge-check", Status: content.StatusPublish, ContentSource: "<p>文章正文。</p>"})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"save"}, "version": {strconv.FormatInt(p.Version, 10)},
		"type": {"post"}, "status": {"publish"}, "title": {p.Title}, "slug": {p.Slug}, "content_source": {p.ContentSource},
		"published_timezone": {"Asia/Shanghai"}, "comment_status": {"open"}, "article_badge_kind": {"original"},
		"article_badge_text": {"<原创>😀"}, "article_badge_style": {"ribbon"}, "article_badge_tone": {"blue"},
		"article_badge_icon": {"newspaper"}, "article_badge_placement": {"top-right"}, "article_badge_hide_mobile": {"true"},
	}
	path := fmt.Sprintf("/admin/content/%d/edit/", p.ID)
	res := serveContentForm(handler, cookie, "https://example.test"+path, "https://example.test", form)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", res.Code, res.Body.String())
	}
	p, _ = repo.GetByID(t.Context(), p.ID)
	if b := p.ArticleBadge(); b.Text != "<原创>😀" || b.Style != "ribbon" || !b.HideOnMobile {
		t.Fatal("save lost fields", b)
	}
	for _, path := range []string{"/", "/explore/?sort=updated", "/posts/badge-check"} {
		res := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, nil, nil)
		if res.Code != 200 || !strings.Contains(res.Body.String(), `np-article-badge-mobile-hidden`) || !strings.Contains(res.Body.String(), "&lt;原创&gt;😀") || strings.Contains(res.Body.String(), "><原创>") {
			t.Fatalf("public route %s badge missing/unsafe: %d", path, res.Code)
		}
	}
	edit := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, cookie, nil)
	if edit.Code != 200 || !strings.Contains(edit.Body.String(), `name="article_badge_text" value="&lt;原创&gt;😀"`) || strings.Count(edit.Body.String(), `data-article-badge-preset=`) != 25 {
		t.Fatal("editor did not restore badge/gallery", edit.Code)
	}
	if publicPost(p).ArticleBadge.Text != p.ArticleBadge().Text {
		t.Fatal("public/v1 lost badge")
	}
	api := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/posts", nil, nil)
	var data struct {
		Items []publicPostResponse `json:"items"`
	}
	if json.Unmarshal(api.Body.Bytes(), &data) != nil || len(data.Items) == 0 || data.Items[0].ArticleBadge.Text != p.ArticleBadge().Text {
		t.Fatal("public API lost badge")
	}
	form.Set("version", strconv.FormatInt(p.Version, 10))
	form.Set("article_badge_text", "恢复点角标")
	form.Set("action", "autosave")
	for _, key := range []string{"excerpt", "cover_url", "tags", "published_at", "published_source"} {
		if !form.Has(key) {
			form.Set(key, "")
		}
	}
	res = serveContentForm(handler, cookie, fmt.Sprintf("https://example.test/admin/content/%d/autosave/", p.ID), "https://example.test", form)
	if res.Code != 200 {
		t.Fatalf("autosave: %d %s", res.Code, res.Body.String())
	}
	owner, _ := autosaveOwnerKey(principal)
	saved, err := repo.GetAutosave(t.Context(), p.ID, owner)
	if err != nil || saved.Snapshot.ArticleBadge().Text != "恢复点角标" {
		t.Fatal("autosave lost badge", err)
	}
	canonical, _ := repo.GetByID(t.Context(), p.ID)
	if canonical.ArticleBadge().Text != p.ArticleBadge().Text {
		t.Fatal("autosave changed canonical badge")
	}
	// Existing clients omit the badge fields: their recovery snapshots retain it.
	in := inputFromPost(p)
	in.ArticleBadge = nil
	snapshot, fields, err := s.buildAutosaveSnapshot(httptest.NewRequest(http.MethodGet, "https://example.test/", nil), p, in, time.Now())
	if err != nil || len(fields) != 0 || snapshot.ArticleBadge() != p.ArticleBadge() {
		t.Fatal("legacy autosave lost badge", err, fields)
	}
	form.Set("article_badge_tone", "unsafe")
	form.Set("action", "save")
	res = serveContentForm(handler, cookie, "https://example.test"+path, "https://example.test", form)
	if res.Code != 422 || !strings.Contains(res.Body.String(), "恢复点角标") {
		t.Fatal("invalid form should preserve input", res.Code)
	}
}

func TestArticleBadgeAPIUpdatesOnlyRequestedMetadata(t *testing.T) {
	_, _, repo, _, principal := newAuthenticatedContentServer(t)
	p, err := repo.Create(t.Context(), content.PostInput{Title: "API 角标", ArticleBadge: &content.ArticleBadge{Kind: "featured"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"title":"只修改标题"}`, `{"article_badge":{"kind":"exclusive","style":"seal","tone":"gold"}}`, `{"article_badge":{}}`} {
		var request adminContentMutationAPIRequest
		if err := json.Unmarshal([]byte(raw), &request); err != nil || request.Empty() {
			t.Fatal("bad request", err)
		}
		in, fields := patchAPIInput(p, request, principal, time.Now())
		if len(fields) > 0 {
			t.Fatal(fields)
		}
		next, err := repo.Update(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, in)
		if err != nil {
			t.Fatal(err)
		}
		if request.ArticleBadge == nil && next.ArticleBadge() != p.ArticleBadge() {
			t.Fatal("title-only edit removed badge")
		}
		if request.ArticleBadge != nil && next.ArticleBadge().Kind != request.ArticleBadge.Kind {
			t.Fatal("badge patch did not apply")
		}
		p = next
	}
}

func TestArticleBadgePinFormAutosaveAndAPI(t *testing.T) {
	s, handler, repo, cookie, principal := newAuthenticatedContentServer(t)
	p, err := repo.Create(t.Context(), content.PostInput{Title: "置顶状态", Slug: "pin-state", Status: content.StatusPublish, ArticleBadge: &content.ArticleBadge{Kind: "original"}})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf_token": {principal.CSRFToken}, "action": {"save"}, "version": {strconv.FormatInt(p.Version, 10)},
		"type": {"post"}, "status": {"publish"}, "title": {p.Title}, "slug": {p.Slug}, "content_source": {p.ContentSource},
		"published_timezone": {"Asia/Shanghai"}, "comment_status": {"open"}, "pinned": {"true"},
	}
	path := fmt.Sprintf("https://example.test/admin/content/%d/edit/", p.ID)
	res := serveContentForm(handler, cookie, path, "https://example.test", form)
	if res.Code != http.StatusSeeOther {
		t.Fatal("pin form", res.Code, res.Body.String())
	}
	p, err = repo.GetByID(t.Context(), p.ID)
	if err != nil || !p.IsPinned() || p.ArticleBadge().Kind != "pinned" || content.ArticleBadgeConfig(p).Kind != "original" {
		t.Fatal("pin lost manual selection", p, err)
	}
	edit := serveCookieRequest(handler, http.MethodGet, path, cookie, nil)
	if edit.Code != http.StatusOK || !strings.Contains(edit.Body.String(), `value="true" selected>置顶`) || !strings.Contains(edit.Body.String(), `已置顶，自动优先显示置顶角标`) {
		t.Fatal("pin state not restored in editor", edit.Code)
	}
	for _, route := range []string{"/", "/explore/?sort=published", "/posts/pin-state"} {
		res = serveCookieRequest(handler, http.MethodGet, "https://example.test"+route, nil, nil)
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `np-article-badge-text">置顶`) || !strings.Contains(res.Body.String(), `chunnel-article-badges.css`) {
			t.Fatal("automatic pin not rendered", route, res.Code)
		}
	}
	if !publicPost(p).Pinned || !adminContentResponse(p, time.Now()).Pinned {
		t.Fatal("API pin projection")
	}
	form.Set("action", "autosave")
	form.Set("version", strconv.FormatInt(p.Version, 10))
	form.Set("pinned", "false")
	for _, key := range []string{"excerpt", "cover_url", "tags", "published_at", "published_source"} {
		form.Set(key, "")
	}
	res = serveContentForm(handler, cookie, fmt.Sprintf("https://example.test/admin/content/%d/autosave/", p.ID), "https://example.test", form)
	if res.Code != http.StatusOK {
		t.Fatal("unpin autosave", res.Code, res.Body.String())
	}
	owner, _ := autosaveOwnerKey(principal)
	saved, err := repo.GetAutosave(t.Context(), p.ID, owner)
	if err != nil || saved.Snapshot.IsPinned() || saved.Snapshot.ArticleBadge().Kind != "original" {
		t.Fatal("autosave pin projection", err)
	}
	canonical, _ := repo.GetByID(t.Context(), p.ID)
	if !canonical.IsPinned() {
		t.Fatal("autosave unpinned live article")
	}
	in := inputFromPost(p)
	in.Pinned = nil
	snapshot, fields, err := s.buildAutosaveSnapshot(httptest.NewRequest(http.MethodGet, "https://example.test/", nil), p, in, time.Now())
	if err != nil || len(fields) != 0 || !snapshot.IsPinned() {
		t.Fatal("legacy autosave lost pin", err, fields)
	}
	for _, raw := range []string{`{"title":"仍然置顶"}`, `{"pinned":false}`, `{"pinned":true}`} {
		var request adminContentMutationAPIRequest
		if err := json.Unmarshal([]byte(raw), &request); err != nil || request.Empty() {
			t.Fatal("pin API request", err)
		}
		input, fields := patchAPIInput(p, request, principal, time.Now())
		if len(fields) != 0 {
			t.Fatal(fields)
		}
		next, err := repo.Update(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, input)
		if err != nil {
			t.Fatal(err)
		}
		want := p.IsPinned()
		if request.Pinned != nil {
			want = *request.Pinned
		}
		if next.IsPinned() != want || content.ArticleBadgeConfig(next).Kind != "original" {
			t.Fatal("pin API lost state or manual config")
		}
		p = next
	}
	form.Set("action", "save")
	form.Set("version", strconv.FormatInt(p.Version, 10))
	res = serveContentForm(handler, cookie, path, "https://example.test", form)
	if res.Code != http.StatusSeeOther {
		t.Fatal("unpin form", res.Code)
	}
	p, _ = repo.GetByID(t.Context(), p.ID)
	if p.IsPinned() || p.ArticleBadge().Kind != "original" {
		t.Fatal("unpin form did not restore original badge")
	}
	form.Set("version", strconv.FormatInt(p.Version, 10))
	form.Set("pinned", "yes")
	res = serveContentForm(handler, cookie, path, "https://example.test", form)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatal("invalid pin flag accepted", res.Code)
	}
}
