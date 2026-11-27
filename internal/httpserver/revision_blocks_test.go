package httpserver

import (
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"newpress.local/newpress/internal/content"
)

func TestRevisionSourceBlocksKeepExactBoundaries(t *testing.T) {
	parts := []string{
		" \n<h2>标题</h2>",
		`<p title='原属性'>正文 &amp; <strong>行内</strong><br>继续</p>`,
		"\n<ul><li>一</li><li>二<ul><li>嵌套</li></ul></li></ul>",
		"<pre><code>if x &lt; 1:\n  print(x)\n</code></pre>",
		`<table><tbody><tr><th>项目</th><th>值</th></tr><tr><td>一</td><td>1</td></tr></tbody></table>`,
		`<figure><img src="/media/test.png" alt="图片"><figcaption>说明</figcaption></figure>`,
		`<aside data-callout="warning"><p>提示正文</p></aside>`,
		"<!-- 原注释 -->",
		`<div data-legacy='true'><p>保留格式</p></div>` + "\r\n ",
	}
	source := strings.Join(parts, "")
	blocks, reason := revisionSourceBlocks(source)
	if reason != "" || len(blocks) != len(parts) {
		t.Fatalf("blocks=%d reason=%q", len(blocks), reason)
	}
	labels := []string{"标题", "段落", "无序列表", "代码", "表格", "图片 / 图文", "提示", "保留 HTML", "保留 HTML"}
	for i, block := range blocks {
		if block.Source != parts[i] || block.Label != labels[i] {
			t.Fatalf("block %d = %#v; source=%q label=%q", i, block, parts[i], labels[i])
		}
	}
}

func TestRevisionBlockDiffCountsWholeBlocksAndExactBytes(t *testing.T) {
	a, b, c := "<p>甲</p>", "<p>乙</p>", "<p>丙</p>"
	long := "<pre><code>" + strings.Repeat("old\n", 100) + "</code></pre>"
	longNew := strings.ReplaceAll(long, "old", "new")
	tests := []struct {
		name, from, to          string
		added, removed, changed int
	}{
		{"same", a + b, a + b, 0, 0, 0},
		{"new block", a + c, a + b + c, 1, 0, 0},
		{"removed block", a + b + c, a + c, 0, 1, 0},
		{"multiline code", a + long + c, a + longNew + c, 0, 0, 1},
		{"reorder", a + b + c, b + a + c, 1, 1, 0},
		{"duplicates", a + a + c, a + c, 0, 1, 0},
		{"attribute", `<p title='old'>甲</p>`, `<p title='new'>甲</p>`, 0, 0, 1},
		{"entity spelling", `<p>&amp;</p>`, `<p>&#38;</p>`, 0, 0, 1},
		{"leading whitespace", a, " \n" + a, 0, 0, 1},
		{"between whitespace", a + b, a + "\n" + b, 0, 0, 1},
		{"trailing whitespace", a, a + " ", 0, 0, 1},
		{"whitespace document", " \n", "\n ", 0, 0, 1},
		{"empty", "", "", 0, 0, 0},
		{"created", "", a, 1, 0, 0},
		{"cleared", a, "", 0, 1, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := buildRevisionBlockDiff(test.from, test.to)
			if !view.Available || view.AddedCount != test.added || view.RemovedCount != test.removed || view.ReplacedCount != test.changed {
				t.Fatalf("view=%#v", view)
			}
			if repeat := buildRevisionBlockDiff(test.from, test.to); !reflect.DeepEqual(view, repeat) {
				t.Fatal("block matching is not deterministic")
			}
		})
	}
}

func TestRevisionBlockDiffFallsBackWithoutPartialClaims(t *testing.T) {
	for _, source := range []string{
		"旧正文没有块标签", "text<p>一</p>", "<p>没有结束", "<p>x</div>",
		"<p>x</p><", `<p title="unfinished>`, "<p><div>隐式闭合</div></p>",
		"<p />", "</br>", "<!-- 未结束", "<!DOCTYPE html><p>x</p>",
		"<svg><path /></svg>", "<math><mi>x</mi></math>", string([]byte{0xff}),
		strings.Repeat("<div>", revisionBlockMaxDepth+1) + "x" + strings.Repeat("</div>", revisionBlockMaxDepth+1),
	} {
		view := buildRevisionBlockDiff("<p>健康正文</p>", source)
		if view.Available || view.Unavailable == "" || len(view.Rows) != 0 || view.FromCount != 0 || view.ToCount != 0 {
			t.Fatalf("partial block comparison for %q: %#v", source, view)
		}
	}
	for _, source := range []string{
		strings.Repeat("<p>x</p>", revisionBlockMaxCount+1),
		"<p>" + strings.Repeat("x", revisionDiffMaxSourceBytes-6) + "</p>",
	} {
		view := buildRevisionBlockDiff(source, "<p>短正文</p>")
		if view.Available || !strings.Contains(view.Unavailable, "64 KiB") || len(view.Rows) != 0 {
			t.Fatalf("unbounded or partially truncated blocks: %#v", view)
		}
	}
	for _, source := range []string{
		strings.Repeat("<p>x</p>", revisionBlockMaxCount),
		"<p>" + strings.Repeat("x", revisionDiffMaxSourceBytes-7) + "</p>",
		strings.Repeat("<div>", revisionBlockMaxDepth) + "x" + strings.Repeat("</div>", revisionBlockMaxDepth),
	} {
		if blocks, reason := revisionSourceBlocks(source); reason != "" || len(blocks) == 0 {
			t.Fatalf("valid boundary rejected: blocks=%d reason=%s", len(blocks), reason)
		}
	}
}

func TestRevisionBlockDiffCollapsesContextAndBoundsPreview(t *testing.T) {
	from := strings.Repeat("<p>未变化</p>", 20) + "<p>旧内容</p>" + strings.Repeat("<p>未变化</p>", 20)
	to := strings.Replace(from, "旧内容", strings.Repeat("新", 200), 1)
	view := buildRevisionBlockDiff(from, to)
	if !view.Available || view.ReplacedCount != 1 || len(view.Rows) != 11 {
		t.Fatalf("context was not collapsed: %#v", view)
	}
	if view.Rows[2].Collapsed != 16 || view.Rows[8].Collapsed != 16 {
		t.Fatalf("incorrect hidden context: %#v", view.Rows)
	}
	changed := view.Rows[5]
	if changed.From.Number != 21 || changed.To.Number != 21 || !strings.Contains(changed.To.Source, strings.Repeat("新", 200)) ||
		utf8.RuneCountInString(changed.To.Preview) != revisionBlockMaxPreviewRunes+1 || !strings.HasSuffix(changed.To.Preview, "…") {
		t.Fatal("preview truncation changed the original block or its position", changed)
	}
}

func TestAdminRevisionBlockCompareEscapesSourceWithoutWriting(t *testing.T) {
	_, handler, repository, cookie, _ := newAuthenticatedContentServer(t)
	original := `<h2>核对</h2><p>原正文</p><script>window.revisionInjected=1</script>`
	post, err := repository.Create(t.Context(), content.PostInput{Title: "块级核对", Slug: "block-compare",
		Type: content.TypePost, Status: content.StatusDraft, ContentSource: original, Actor: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	post, err = repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: "editor"},
		content.PostInput{Title: post.Title, Slug: post.Slug, Type: post.Type, Status: post.Status,
			ContentSource: strings.Replace(original, "原正文", "新正文", 1)})
	if err != nil {
		t.Fatal(err)
	}
	before, err := repository.ListRevisions(t.Context(), post.ID, 20)
	if err != nil || len(before) != 2 {
		t.Fatal("missing revisions", err)
	}
	url := "https://example.test/admin/content/" + strconv.FormatInt(post.ID, 10) + "/revisions/compare/?from=" + strconv.FormatInt(before[1].ID, 10) + "&to=current"
	response := serveCookieRequest(handler, http.MethodGet, url, cookie, nil)
	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(body, "data-revision-block-diff") || !strings.Contains(body, "内容块差异") ||
		!strings.Contains(body, "&lt;script&gt;window.revisionInjected=1&lt;/script&gt;") ||
		strings.Contains(body, `<script>window.revisionInjected=1</script>`) {
		t.Fatalf("unsafe or absent block view: %d %s", response.Code, body)
	}
	afterPost, err := repository.GetByID(t.Context(), post.ID)
	if err != nil || !reflect.DeepEqual(afterPost, post) {
		t.Fatal("comparison changed current content", err)
	}
	after, err := repository.ListRevisions(t.Context(), post.ID, 20)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("comparison changed historical snapshots", err)
	}
}
