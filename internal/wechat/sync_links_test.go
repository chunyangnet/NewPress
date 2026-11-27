package wechat

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestSyncLinksResolveAgainstOriginalArticle(t *testing.T) {
	const source = `<base href="https://untrusted.test/"><p><a href="next">下一篇</a><a href="./next?q=1&amp;x=2#part">同目录</a><a href="../series/read">上层</a><a href="?view=print#part">查询</a><a href="#chapter">章节</a><a href="/explore/?q=writing">根目录</a><a href="//external.test/read">省略协议</a><a href="https://external.test/absolute?q=1#part">外链</a><a href="mailto:editor@example.test">邮件</a></p>`
	for _, row := range []struct {
		name, base, slug, article string
		published, sourceLink     bool
	}{
		{"published", "https://example.test", "original", "https://example.test/posts/original", true, true},
		{"trailing-slash", "https://example.test/", "original", "https://example.test/posts/original", true, true},
		{"draft", "https://example.test", "original", "https://example.test/posts/original", false, true},
		{"source-disabled", "https://example.test", "original", "https://example.test/posts/original", true, false},
		{"unicode-slug", "https://example.test", "文章", "https://example.test/posts/%E6%96%87%E7%AB%A0", true, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			post := content.Post{Type: content.TypePost, Slug: row.slug, Title: "链接", Status: content.StatusDraft, ContentHTML: contenthtml.Sanitize(source)}
			if row.published {
				post.Status, post.PublishedAt = content.StatusPublish, time.Now().Add(-time.Hour)
			}
			cfg := Defaults()
			cfg.IncludeSource, cfg.ThumbMediaID = row.sourceLink, "existing-cover"
			plan, err := buildSyncPlan(cfg, post, row.base)
			if err != nil || len(plan.issues) != 0 {
				t.Fatal(plan, err)
			}
			var got []string
			for _, link := range collectedNodes(t, plan.article.Content, "a") {
				got = append(got, collectedAttr(link, "href"))
			}
			want := []string{"https://example.test/posts/next", "https://example.test/posts/next?q=1&x=2#part", "https://example.test/series/read", row.article + "?view=print#part", row.article + "#chapter", "https://example.test/explore/?q=writing", "https://external.test/read", "https://external.test/absolute?q=1#part", "mailto:editor@example.test"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("outgoing links:\n%q\nwant:\n%q", got, want)
			}
			if plan.article.SourceURL == "" && row.published && row.sourceLink || plan.article.SourceURL != "" && (!row.published || !row.sourceLink) {
				t.Fatalf("source entry policy changed: %q", plan.article.SourceURL)
			}
			if post.ContentHTML != contenthtml.Sanitize(source) {
				t.Fatal("sync changed original content")
			}
		})
	}
}

func TestSyncExpandedArticleLinksEnforceBodyLimitBeforeRemoteWrites(t *testing.T) {
	s, post := fixture(t)
	post, err := s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{
		Type: post.Type, Status: post.Status, Title: post.Title, Slug: strings.Repeat("long-", 16) + "article",
		ContentSource: strings.Repeat(`<p><a href="#part">章节</a></p>`, 300),
	})
	if err != nil {
		t.Fatal(err)
	}
	remote := 0
	s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		remote++
		return nil, errors.New("unexpected remote request")
	})}
	state, err := s.Store.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	check, err := s.CheckSync(t.Context(), post.ID, post.Version, state.Config.Version)
	if err != nil || check.Ready || check.ContentChars < 20000 || len(check.Issues) != 1 || check.Issues[0].Field != "content" {
		t.Fatalf("expanded links escaped local limit: %+v, %v", check, err)
	}
	if _, err = s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	job := stateJob(t, s)
	if job.Status != "failed" || !strings.Contains(job.Error, "2 万字符") || remote != 0 || job.MediaID != "" {
		t.Fatalf("oversized links reached remote API: %+v, calls=%d", job, remote)
	}
}
