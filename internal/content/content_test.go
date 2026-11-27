package content

import (
	"context"
	"errors"
	"html"
	"strings"
	"testing"
	"time"

	htmlparser "golang.org/x/net/html"
)

func TestImageMetadataSurvivesContentSanitization(t *testing.T) {
	t.Parallel()
	alt := `后台界面 "引号" & <内容> " onerror="alert(2)`
	caption := "图片说明 <script>alert(1)</script> & 第二行\n保留换行"
	source := `<figure><img src="/media/1/test.png" alt="` + html.EscapeString(alt) + `" width="320" height="211" onerror="alert(3)" style="position:fixed"><figcaption>` + html.EscapeString(caption) + `<script>alert(4)</script></figcaption></figure><img src="/media/2/decorative.png" alt=""><img src="javascript:alert(5)">`
	input, fields := NormalizePostInput(PostInput{Title: "Images", Slug: "images", ContentSource: source}, time.Now())
	if len(fields) != 0 {
		t.Fatalf("image content validation: %v", fields)
	}
	document, err := htmlparser.Parse(strings.NewReader(input.ContentHTML))
	if err != nil {
		t.Fatal(err)
	}
	images := make(map[string]map[string]string)
	var captionText strings.Builder
	var visit func(*htmlparser.Node, bool)
	visit = func(node *htmlparser.Node, inCaption bool) {
		if node.Type == htmlparser.ElementNode {
			if node.Data == "script" {
				t.Fatal("script element survived sanitization")
			}
			attrs := make(map[string]string)
			for _, attr := range node.Attr {
				if strings.HasPrefix(attr.Key, "on") || attr.Key == "style" || strings.HasPrefix(attr.Val, "javascript:") {
					t.Fatalf("unsafe attribute survived: %#v", attr)
				}
				attrs[attr.Key] = attr.Val
			}
			if node.Data == "img" {
				images[attrs["src"]] = attrs
			}
			inCaption = inCaption || node.Data == "figcaption"
		}
		if inCaption && node.Type == htmlparser.TextNode {
			captionText.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child, inCaption)
		}
	}
	visit(document, false)
	image := images["/media/1/test.png"]
	if image["alt"] != alt || image["width"] != "320" || image["height"] != "211" || captionText.String() != caption {
		t.Fatalf("image metadata changed: image=%#v, caption=%q", image, captionText.String())
	}
	if value, ok := images["/media/2/decorative.png"]["alt"]; !ok || value != "" {
		t.Fatalf("decorative image lost empty alt: %#v", images)
	}
}

func TestNormalizePostInputSanitizesAndSchedules(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	input, fields := NormalizePostInput(PostInput{
		Type: TypePost, Status: StatusPublish, Title: "  安全的内容核心  ", Slug: " Secure_Core ",
		ContentSource: `<p>正文 <strong>保留</strong></p><script>alert(1)</script><a href="javascript:alert(2)">链接</a>`,
		PublishedAt:   now.Add(time.Hour), CoverURL: "/media/cover.jpg",
	}, now)
	if len(fields) != 0 {
		t.Fatalf("unexpected fields: %#v", fields)
	}
	if input.Slug != "secure-core" {
		t.Fatalf("slug = %q", input.Slug)
	}
	if input.Status != StatusFuture {
		t.Fatalf("status = %q, want future", input.Status)
	}
	if strings.Contains(input.ContentHTML, "script") || strings.Contains(input.ContentHTML, "javascript:") {
		t.Fatalf("unsafe HTML survived: %s", input.ContentHTML)
	}
	if !strings.Contains(input.ContentHTML, "<strong>保留</strong>") {
		t.Fatalf("safe formatting was removed: %s", input.ContentHTML)
	}
	if input.Excerpt == "" {
		t.Fatal("excerpt was not generated")
	}

	plain, fields := NormalizePostInput(PostInput{Title: "Plain", Slug: "plain", ContentSource: "第一段\n仍在第一段\n\n第二段"}, now)
	if len(fields) != 0 || plain.ContentHTML != "<p>第一段<br>仍在第一段</p><p>第二段</p>" {
		t.Fatalf("plain content = %q, fields=%#v", plain.ContentHTML, fields)
	}
}

func TestNormalizePostInputRejectsExternalCover(t *testing.T) {
	t.Parallel()
	_, fields := NormalizePostInput(PostInput{
		Type: TypePost, Status: StatusDraft, Title: "External cover", Slug: "external-cover",
		ContentSource: "body", CoverURL: "https://cdn.example.test/cover.jpg", CommentStatus: CommentOpen,
	}, time.Now())
	if message := fields["cover_url"]; !strings.Contains(message, "仅允许站内绝对路径") {
		t.Fatalf("cover error = %q", message)
	}
}

func TestGeneratedExcerptPreservesReadableText(t *testing.T) {
	cases := []struct {
		name, source, want string
	}{
		{"entities", `<p>A &amp; B &#x1f600;&nbsp;C</p>`, "A & B \U0001f600 C"},
		{"blocks", `<h2>标题</h2><p>第一<strong>段</strong></p><p>第二<br>行</p><ul><li>一</li><li>二</li></ul>`, "标题 第一段 第二 行 一 二"},
		{"literal code", `<p><code>&lt;demo&gt;&amp;</code> &amp;lt;literal&amp;gt;</p>`, "<demo>& &lt;literal&gt;"},
		{"unsafe source", `<script>alert(1)</script><p>公开正文</p><style>body{display:none}</style>`, "公开正文"},
		{"unicode truncation", "<p>" + strings.Repeat("界", 179) + "\U0001f600结束</p>", strings.Repeat("界", 179) + "\U0001f600…"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input, fields := NormalizePostInput(PostInput{Title: "Excerpt", Slug: "excerpt", ContentSource: test.source}, time.Now())
			if len(fields) != 0 || input.Excerpt != test.want {
				t.Fatalf("excerpt = %q, want %q; fields=%v", input.Excerpt, test.want, fields)
			}
		})
	}
	input, fields := NormalizePostInput(PostInput{Title: "Custom", Slug: "custom", ContentSource: "body", Excerpt: "自定义 &amp; 摘要"}, time.Now())
	if len(fields) != 0 || input.Excerpt != "自定义 &amp; 摘要" {
		t.Fatalf("custom excerpt changed: %q, fields=%v", input.Excerpt, fields)
	}
}

func TestNormalizePostInputRejectsUnsafeFields(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	_, fields := NormalizePostInput(PostInput{
		Type: "attachment", Status: "execute", Title: "bad\x00title", Slug: "../admin",
		CoverURL: "javascript:alert(1)", CommentStatus: "maybe",
		ContentSource: strings.Repeat("x", MaxContentBytes+1),
	}, now)
	for _, field := range []string{"type", "status", "title", "cover_url", "comment_status", "content_source"} {
		if fields[field] == "" {
			t.Fatalf("missing validation error for %s: %#v", field, fields)
		}
	}
}

func TestMemoryRepositoryContentLifecycleAndOptimisticLock(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository(false)
	repository.now = func() time.Time { return now }

	created, err := repository.Create(ctx, PostInput{
		Type: TypePost, Status: StatusDraft, Slug: "editorial-roadmap", Title: "编辑路线图",
		ContentSource: "第一版正文", Category: "项目日志", AuthorName: "admin", Actor: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.Status != StatusDraft || created.ID == 0 {
		t.Fatalf("unexpected created post: %#v", created)
	}
	if posts, _ := repository.ListPublished(ctx, 20); len(posts) != 0 {
		t.Fatalf("draft leaked into public list: %#v", posts)
	}

	published, err := repository.Update(ctx, created.ID, Mutation{ExpectedVersion: 1, Actor: "admin"}, PostInput{
		Type: TypePost, Status: StatusPublish, Slug: created.Slug, Title: created.Title,
		ContentSource: created.ContentSource, Category: created.Category, AuthorName: created.AuthorName,
	})
	if err != nil {
		t.Fatal(err)
	}
	if published.Version != 2 || published.PublishedAt != now {
		t.Fatalf("unexpected published post: %#v", published)
	}
	if _, err := repository.GetPublishedBySlug(ctx, created.Slug); err != nil {
		t.Fatalf("published post unavailable: %v", err)
	}

	_, err = repository.Update(ctx, created.ID, Mutation{ExpectedVersion: 1}, PostInput{
		Type: TypePost, Status: StatusDraft, Slug: created.Slug, Title: "stale",
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update error = %v", err)
	}

	_, err = repository.Create(ctx, PostInput{Type: TypePost, Status: StatusDraft, Slug: created.Slug, Title: "duplicate"})
	if !errors.Is(err, ErrSlugConflict) {
		t.Fatalf("duplicate slug error = %v", err)
	}
	page, err := repository.Create(ctx, PostInput{Type: TypePage, Status: StatusPublish, Slug: created.Slug, Title: "同名页面"})
	if err != nil {
		t.Fatalf("same slug on another type should be allowed: %v", err)
	}
	if _, err := repository.GetPublishedPageBySlug(ctx, page.Slug); err != nil {
		t.Fatalf("published page unavailable: %v", err)
	}

	now = now.Add(time.Minute)
	changed, err := repository.Update(ctx, created.ID, Mutation{ExpectedVersion: 2, Actor: "editor"}, PostInput{
		Type: TypePost, Status: StatusPublish, Slug: created.Slug, Title: "编辑路线图 2.0",
		ContentSource: "第二版正文", Category: "项目日志", AuthorName: "admin", PublishedAt: published.PublishedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := repository.ListRevisions(ctx, created.ID, 20)
	if err != nil || len(revisions) != 3 {
		t.Fatalf("revisions = %#v, err=%v", revisions, err)
	}
	var previous Revision
	for _, revision := range revisions {
		if revision.Version == 2 {
			previous = revision
		}
	}
	if previous.ID == 0 {
		t.Fatal("version 2 revision was not found")
	}
	loadedRevision, err := repository.GetRevision(ctx, created.ID, previous.ID)
	if err != nil || loadedRevision.PostID != created.ID || loadedRevision.Version != previous.Version {
		t.Fatalf("get revision = %#v, err=%v", loadedRevision, err)
	}
	loadedRevision.Snapshot.Title = "mutated caller copy"
	reloadedRevision, err := repository.GetRevision(ctx, created.ID, previous.ID)
	if err != nil || reloadedRevision.Snapshot.Title == loadedRevision.Snapshot.Title {
		t.Fatalf("revision snapshot was not cloned = %#v, err=%v", reloadedRevision, err)
	}
	if _, err := repository.GetRevision(ctx, page.ID, previous.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revision escaped post binding: %v", err)
	}
	restored, err := repository.RestoreRevision(ctx, created.ID, previous.ID, Mutation{ExpectedVersion: changed.Version, Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Version != 4 || restored.Title != created.Title || restored.Status != StatusPublish {
		t.Fatalf("unexpected restored post: %#v", restored)
	}

	trashed, err := repository.ChangeStatus(ctx, created.ID, Mutation{ExpectedVersion: restored.Version, Actor: "admin"}, StatusTrash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetPublishedBySlug(ctx, created.Slug); !errors.Is(err, ErrNotFound) {
		t.Fatalf("trashed post remained public: %v", err)
	}
	if _, err := repository.ChangeStatus(ctx, created.ID, Mutation{ExpectedVersion: trashed.Version, Actor: "admin"}, StatusDraft); err != nil {
		t.Fatalf("restore to draft: %v", err)
	}

	list, err := repository.ListAdmin(ctx, AdminQuery{Type: TypePost, Status: StatusDraft, Search: "路线图", Limit: 20})
	if err != nil || list.Total != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("admin list = %#v, err=%v", list, err)
	}
}

func TestMemoryRepositoryScheduledVisibility(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository(false)
	repository.now = func() time.Time { return now }
	future := now.Add(2 * time.Hour)
	post, err := repository.Create(ctx, PostInput{
		Type: TypePost, Status: StatusPublish, Slug: "scheduled", Title: "定时文章", PublishedAt: future,
	})
	if err != nil || post.Status != StatusFuture {
		t.Fatalf("scheduled create = %#v, err=%v", post, err)
	}
	if posts, _ := repository.ListPublished(ctx, 20); len(posts) != 0 {
		t.Fatal("scheduled post became visible too early")
	}
	now = future
	posts, err := repository.ListPublished(ctx, 20)
	if err != nil || len(posts) != 1 || posts[0].ID != post.ID {
		t.Fatalf("due scheduled post = %#v, err=%v", posts, err)
	}
}

func TestMemoryDemoPostsDoNotClaimPersistentUserIDs(t *testing.T) {
	t.Parallel()
	repository := NewMemoryRepository(true)
	all, err := repository.ListAdmin(t.Context(), AdminQuery{Limit: 20})
	if err != nil || all.Total != 9 {
		t.Fatalf("demo list = %#v, err=%v", all, err)
	}
	for _, post := range all.Items {
		if post.AuthorID != 0 {
			t.Fatalf("demo post %d claimed persistent author id %d", post.ID, post.AuthorID)
		}
		if strings.TrimSpace(post.AuthorName) == "" {
			t.Fatalf("demo post %d lost its display byline", post.ID)
		}
	}
	owned, err := repository.ListAdmin(t.Context(), AdminQuery{AuthorID: 2, Limit: 20})
	if err != nil || owned.Total != 0 || len(owned.Items) != 0 {
		t.Fatalf("new persistent user inherited demo content: %#v, err=%v", owned, err)
	}
}

func TestMemoryRepositoryImmediatePublishOverridesFutureTimestamp(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 9, 30, 0, 0, time.UTC)
	repository := NewMemoryRepository(false)
	repository.now = func() time.Time { return now }
	post, err := repository.Create(t.Context(), PostInput{
		Type: TypePost, Status: StatusFuture, Slug: "publish-now", Title: "立即发布",
		ContentSource: "正文", PublishedAt: now.Add(24 * time.Hour), CommentStatus: CommentOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	published, err := repository.ChangeStatus(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version, Actor: "admin"}, StatusPublish)
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != StatusPublish || !published.PublishedAt.Equal(now) || !PublicAt(published, now) {
		t.Fatalf("immediate publish = %#v", published)
	}
}

func TestMemoryTaxonomyLifecycleAssignmentAndArchives(t *testing.T) {
	ctx := t.Context()
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository(false)
	repository.now = func() time.Time { return now }

	initial, err := repository.ListTerms(ctx, TermQuery{Taxonomy: TaxonomyCategory, Limit: 100})
	if err != nil || len(initial.Items) != 1 || !initial.Items[0].System || initial.Items[0].Name != "未分类" {
		t.Fatalf("default taxonomy = %#v, err=%v", initial, err)
	}
	parent, err := repository.CreateTerm(ctx, TermInput{Taxonomy: TaxonomyCategory, Name: "工程实践", Slug: "工程 实践", Description: "生产工程路线"})
	if err != nil || parent.Slug != "工程-实践" || parent.Version != 1 {
		t.Fatalf("parent term = %#v, err=%v", parent, err)
	}
	child, err := repository.CreateTerm(ctx, TermInput{Taxonomy: TaxonomyCategory, ParentID: parent.ID, Name: "性能优化"})
	if err != nil || child.ParentName != parent.Name || child.Depth != 1 {
		t.Fatalf("child term = %#v, err=%v", child, err)
	}
	tag, err := repository.CreateTerm(ctx, TermInput{Taxonomy: TaxonomyTag, Name: "Go"})
	if err != nil || tag.Taxonomy != TaxonomyTag {
		t.Fatalf("tag = %#v, err=%v", tag, err)
	}

	post, err := repository.Create(ctx, PostInput{
		Type: TypePost, Status: StatusPublish, Slug: "taxonomy-runtime", Title: "分类法运行时",
		ContentSource: "正文", CategoryIDs: []int64{child.ID}, TagNames: []string{"Go", "安全", "Go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(post.Categories) != 1 || post.Categories[0].ID != child.ID || post.Category != child.Name || len(post.Tags) != 2 {
		t.Fatalf("assigned post = %#v", post)
	}
	archiveTerm, posts, total, err := repository.ListPublishedByTerm(ctx, TaxonomyCategory, child.Slug, 20, 0)
	if err != nil || archiveTerm.ID != child.ID || total != 1 || len(posts) != 1 || posts[0].ID != post.ID {
		t.Fatalf("category archive = term=%#v posts=%#v total=%d err=%v", archiveTerm, posts, total, err)
	}
	_, tagPosts, tagTotal, err := repository.ListPublishedByTerm(ctx, TaxonomyTag, tag.Slug, 20, 0)
	if err != nil || tagTotal != 1 || len(tagPosts) != 1 {
		t.Fatalf("tag archive = %#v total=%d err=%v", tagPosts, tagTotal, err)
	}

	updatedParent, err := repository.UpdateTerm(ctx, parent.ID, TermMutation{ExpectedVersion: parent.Version}, TermInput{
		Taxonomy: TaxonomyCategory, Name: "工程体系", Slug: parent.Slug, Description: parent.Description,
	})
	if err != nil || updatedParent.Version != 2 || updatedParent.Name != "工程体系" {
		t.Fatalf("updated parent = %#v, err=%v", updatedParent, err)
	}
	_, err = repository.UpdateTerm(ctx, parent.ID, TermMutation{ExpectedVersion: updatedParent.Version}, TermInput{
		Taxonomy: TaxonomyCategory, ParentID: child.ID, Name: updatedParent.Name, Slug: updatedParent.Slug,
	})
	if !errors.Is(err, ErrTermHierarchyConflict) {
		t.Fatalf("cycle update error = %v", err)
	}

	if err := repository.DeleteTerm(ctx, child.ID, TermMutation{ExpectedVersion: child.Version}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := repository.GetByID(ctx, post.ID)
	if err != nil || len(reloaded.Categories) != 1 || !reloaded.Categories[0].System || reloaded.Category != "未分类" {
		t.Fatalf("category fallback = %#v, err=%v", reloaded, err)
	}
	if err := repository.DeleteTerm(ctx, initial.Items[0].ID, TermMutation{ExpectedVersion: initial.Items[0].Version}); !errors.Is(err, ErrTermProtected) {
		t.Fatalf("protected deletion error = %v", err)
	}
	if _, err := repository.UpdateTerm(ctx, parent.ID, TermMutation{ExpectedVersion: 1}, TermInput{Taxonomy: TaxonomyCategory, Name: "stale", Slug: "stale"}); !errors.Is(err, ErrTermVersionConflict) {
		t.Fatalf("stale term update error = %v", err)
	}
}

func TestNormalizeTaxonomyAndPostTermLimits(t *testing.T) {
	term, fields := NormalizeTermInput(TermInput{Taxonomy: TaxonomyCategory, Name: "  中文 分类  ", Slug: ""})
	if len(fields) != 0 || term.Slug != "中文-分类" || !ValidTermSlug(term.Slug) {
		t.Fatalf("normalized term = %#v fields=%#v", term, fields)
	}
	_, fields = NormalizeTermInput(TermInput{Taxonomy: TaxonomyTag, ParentID: 1, Name: "bad"})
	if fields["parent_id"] == "" {
		t.Fatalf("tag parent validation = %#v", fields)
	}
	_, fields = NormalizePostInput(PostInput{
		Type: TypePost, Title: "Too many terms", Slug: "too-many-terms",
		CategoryIDs: []int64{1, 1, -2}, TagNames: []string{strings.Repeat("标", MaxTermNameRunes+1)},
	}, time.Now())
	if fields["category_ids"] == "" || fields["tags"] == "" {
		t.Fatalf("post term validation = %#v", fields)
	}
}
