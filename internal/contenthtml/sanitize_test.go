package contenthtml

import (
	"strings"
	"testing"
)

func TestStructuredBlocksRetainSemanticsWithoutExecutableAttributes(t *testing.T) {
	for _, source := range []string{
		`<aside data-callout="note"><p>说明</p></aside>`,
		`<aside data-callout="tip"><p>建议</p></aside>`,
		`<aside data-callout="warning"><p>注意</p></aside>`,
		`<pre><code class="language-go">if a &lt; b {}</code></pre>`,
		`<table><thead><tr><th scope="col">项目</th></tr></thead><tbody><tr><td>内容</td></tr></tbody></table>`,
	} {
		if got := Sanitize(source); got != source {
			t.Errorf("structured content changed: %q -> %q", source, got)
		}
		if got := Sanitize(Sanitize(source)); got != Sanitize(source) {
			t.Errorf("render-time sanitization changed stored semantics: %q", got)
		}
	}
	unsafe := `<aside data-callout="warning evil" onclick="evil()" style="position:fixed"><script>alert(1)</script><p>正文</p></aside><code class="language-go evil" onmouseover="evil()">safe</code><a href="javascript:evil()">link</a>`
	got := Sanitize(unsafe)
	for _, forbidden := range []string{"data-callout", "onclick", "style=", "script", "class=", "onmouseover", "javascript:"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("unsafe %s survived: %s", forbidden, got)
		}
	}
}

func TestArticleWidgetSanitizationRetainsOnlySemanticContract(t *testing.T) {
	source := `<aside data-np-widget="post" data-np-post="42" data-np-layout="compact"><a data-np-part="link" href="/posts/example"><span data-np-part="cover"><img src="/media/1/image.png" alt="" loading="lazy" width="240" height="160"></span><span data-np-part="body"><span data-np-part="eyebrow">推荐阅读</span><strong data-np-part="title">标题</strong><span data-np-part="excerpt">摘要</span><span data-np-part="meta">作者</span></span><span data-np-part="arrow" aria-hidden="true">↗</span></a></aside>`
	expected := strings.Replace(source, `href="/posts/example"`, `href="/posts/example" rel="nofollow"`, 1)
	if got := Sanitize(source); got != expected || Sanitize(got) != got {
		t.Fatalf("widget contract changed: %s", got)
	}
	unsafe := `<aside data-np-widget="other" data-np-post="-1" data-np-layout="full" data-secret="hidden" style="position:fixed" class="override" onclick="evil()"><span data-np-part="link">x</span><a data-np-part="body" href="javascript:evil()">x</a><strong data-np-part="arrow">x</strong></aside><p data-np-widget="post" data-np-post="42" data-np-part="title">x</p>`
	got := Sanitize(unsafe)
	for _, forbidden := range []string{"data-np-", "data-secret", "style=", "class=", "onclick", "javascript:"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("unsafe %s survived: %s", forbidden, got)
		}
	}
}
