package content

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArticleBadgePresetsAndValidation(t *testing.T) {
	seen := map[string]bool{}
	for _, group := range ArticleBadgePresets() {
		for _, preset := range group.Presets {
			b, fields := NormalizeArticleBadge(ArticleBadge{Kind: preset.Kind})
			if len(fields) > 0 || b != preset.ArticleBadge || seen[b.Kind] {
				t.Fatalf("invalid or duplicate preset: %+v %v", b, fields)
			}
			seen[b.Kind] = true
		}
	}
	for _, b := range []ArticleBadge{
		{Kind: "<script>"}, {Kind: "original", Text: strings.Repeat("字", 13)},
		{Kind: "original", Text: "a\nb"}, {Kind: "original", Text: string([]byte{0xff})},
		{Kind: "original", Style: `glass" onclick="alert(1)`}, {Kind: "original", Tone: "url(https://example.com)"},
		{Kind: "original", Icon: "../../icon"}, {Kind: "original", Placement: "fixed"},
	} {
		if _, fields := NormalizeArticleBadge(b); len(fields) == 0 {
			t.Fatalf("unsafe badge accepted: %+v", b)
		}
		post := Post{Meta: map[string]any{"article_badge": b}}
		if post.ArticleBadge().Kind != "" {
			t.Fatal("unsafe persisted badge reached the public view")
		}
	}
	b, fields := NormalizeArticleBadge(ArticleBadge{Kind: "custom", Text: " <原创>😀 "})
	if len(fields) != 0 || b.Text != "<原创>😀" {
		t.Fatal("valid Unicode/text should be escaped by templates, not rewritten", b, fields)
	}
}

func TestArticleBadgeFilePersistenceOmissionClearAndRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	in := PostInput{Title: "角标文章", Slug: "article-badge", ArticleBadge: &ArticleBadge{Kind: "original", Text: "原创😀", Placement: "top-right", HideOnMobile: true}}
	p, err := r.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	want := p.ArticleBadge()
	revisions, err := r.ListRevisions(t.Context(), p.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	in.ArticleBadge = nil
	in.Title = "普通客户端的编辑"
	p, err = r.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, in)
	if err != nil || p.ArticleBadge() != want {
		t.Fatalf("omission lost badge: %+v %v", p.ArticleBadge(), err)
	}
	r, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err = r.GetByID(t.Context(), p.ID)
	if err != nil || p.ArticleBadge() != want {
		t.Fatalf("restart lost badge: %+v %v", p.ArticleBadge(), err)
	}
	in.ArticleBadge = &ArticleBadge{}
	p, err = r.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, in)
	if err != nil || p.ArticleBadge().Kind != "" {
		t.Fatal("explicit clearing failed", err)
	}
	p, err = r.RestoreRevision(t.Context(), p.ID, revisions[0].ID, Mutation{ExpectedVersion: p.Version})
	if err != nil || p.ArticleBadge() != want {
		t.Fatal("restoration lost badge", err)
	}
	in.ArticleBadge = &ArticleBadge{Kind: "original", Tone: "invalid"}
	if _, err := r.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, in); err == nil {
		t.Fatal("invalid update accepted")
	}
	unchanged, _ := r.GetByID(t.Context(), p.ID)
	if unchanged.Version != p.Version || unchanged.ArticleBadge() != want {
		t.Fatal("invalid update changed stored data")
	}
}

func TestArticleBadgeStatusUsesCanonicalState(t *testing.T) {
	for status, label := range map[string]string{StatusPublish: "已发布", StatusDraft: "草稿", StatusPending: "待审核", StatusFuture: "定时发布", StatusPrivate: "私密", StatusTrash: "已下架"} {
		p := Post{Status: status, Meta: map[string]any{"article_badge": ArticleBadge{Kind: "status", Text: "错误状态", Tone: "red"}}}
		if b := p.ArticleBadge(); b.Text != label || b.Style != "soft" {
			t.Fatalf("%s: %+v", status, b)
		}
	}
	p := Post{Type: TypePost, Status: StatusFuture, PublishedAt: time.Now().Add(-time.Minute), Meta: map[string]any{"article_badge": ArticleBadge{Kind: "status"}}}
	if p.ArticleBadge().Text != "已发布" {
		t.Fatal("due scheduled article still has a scheduled badge")
	}
}

func TestArticleBadgePinnedTracksStateAndPreservesManualChoice(t *testing.T) {
	p := Post{Type: TypePost, Meta: map[string]any{"article_badge": ArticleBadge{Kind: "pinned", Text: "伪造置顶"}}}
	if p.IsPinned() || p.ArticleBadge().Kind != "" {
		t.Fatal("choosing a preset must not pin an article or show a false state")
	}
	p.Meta["pinned"] = true
	if b := p.ArticleBadge(); b.Kind != "pinned" || b.Text != "置顶" || b.Style != "ribbon" {
		t.Fatal("actual pin state did not drive its badge", b)
	}
	p.Meta["article_badge"] = ArticleBadge{Kind: "original", Placement: "top-right", HideOnMobile: true}
	if b := p.ArticleBadge(); b.Kind != "pinned" || b.Placement != "top-right" || !b.HideOnMobile {
		t.Fatal("automatic pin badge did not preserve placement/mobile preference", b)
	}
	delete(p.Meta, "pinned")
	if p.ArticleBadge().Kind != "original" {
		t.Fatal("unpinning should restore the manual badge")
	}
	p.Meta["pinned"] = "true"
	if p.IsPinned() || p.ArticleBadge().Kind != "original" {
		t.Fatal("malformed metadata became a pin")
	}
	p.Meta["pinned"] = true
	p.Type = TypePage
	if p.IsPinned() || p.ArticleBadge().Kind != "original" {
		t.Fatal("a page participated in article pinning")
	}
}

func TestArticleBadgePinPersistenceOrderingPaginationAndVisibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	pinned := true
	base := time.Now().Add(-24 * time.Hour)
	old, err := r.Create(t.Context(), PostInput{Title: "旧置顶", Slug: "old-pinned", Status: StatusPublish, PublishedAt: base, Pinned: &pinned, ArticleBadge: &ArticleBadge{Kind: "original"}})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := r.Create(t.Context(), PostInput{Title: "新文章", Slug: "new-normal", Status: StatusPublish, PublishedAt: base.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{StatusPrivate, StatusDraft, StatusFuture, StatusTrash} {
		_, err := r.Create(t.Context(), PostInput{Title: "不公开", Slug: "hidden-" + status, Status: status, PublishedAt: time.Now().Add(time.Hour), Pinned: &pinned})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Older clients omit the flag and keep its state, including through reopen.
	old, err = r.Update(t.Context(), old.ID, Mutation{ExpectedVersion: old.Version}, PostInput{Title: old.Title, Slug: old.Slug, Status: old.Status, PublishedAt: old.PublishedAt})
	if err != nil {
		t.Fatal(err)
	}
	r, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	old, err = r.GetByID(t.Context(), old.ID)
	if err != nil || !old.IsPinned() || old.ArticleBadge().Text != "置顶" {
		t.Fatal("pin lost on reopen", err)
	}
	for _, offset := range []int{0, 1} {
		want := []int64{old.ID, newer.ID}[offset]
		page, err := r.ListPublishedPage(t.Context(), 1, offset)
		if err != nil || page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != want {
			t.Fatal("pin must sort before pagination and hide non-public content", page, err)
		}
		page, err = r.SearchPublished(t.Context(), PublishedQuery{Limit: 1, Offset: offset})
		if err != nil || page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != want {
			t.Fatal("search pin order", page, err)
		}
	}
	_, items, total, err := r.ListPublishedByTerm(t.Context(), TaxonomyCategory, old.Categories[0].Slug, 1, 0)
	if err != nil || total != 2 || len(items) != 1 || items[0].ID != old.ID {
		t.Fatal("archive pin order", items, err)
	}
	page, err := r.SearchPublished(t.Context(), PublishedQuery{Sort: "selection", PostIDs: []int64{newer.ID, old.ID}})
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != newer.ID {
		t.Fatal("explicit selection order changed", page, err)
	}
	for _, sort := range []string{"updated", "title"} {
		// Check that explicit sorts remain valid and retain all public results.
		page, err = r.SearchPublished(t.Context(), PublishedQuery{Sort: sort})
		if err != nil || page.Total != 2 {
			t.Fatal("explicit ordering", sort, err)
		}
	}
	revisions, err := r.ListRevisions(t.Context(), old.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	pinned = false
	old, err = r.Update(t.Context(), old.ID, Mutation{ExpectedVersion: old.Version}, PostInput{Title: old.Title, Slug: old.Slug, Status: old.Status, PublishedAt: old.PublishedAt, Pinned: &pinned})
	if err != nil || old.IsPinned() || old.ArticleBadge().Kind != "original" {
		t.Fatal("unpin did not restore original badge", old, err)
	}
	page, err = r.ListPublishedPage(t.Context(), 1, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != newer.ID {
		t.Fatal("unpin did not restore chronological order", page, err)
	}
	old, err = r.RestoreRevision(t.Context(), old.ID, revisions[0].ID, Mutation{ExpectedVersion: old.Version})
	if err != nil || !old.IsPinned() {
		t.Fatal("revision did not restore pin", err)
	}
}
