package wechat

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/content"
)

func collectedNodes(t *testing.T, source, tag string) []*html.Node {
	t.Helper()
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	var result []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == tag {
			result = append(result, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return result
}

func collectedAttr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func TestCollectPreservesImageDescriptionsThroughPreviewAndImport(t *testing.T) {
	source := `<meta property="og:title" content="图文"><div id="js_content"><figure><img data-src="https://mmbiz.qpic.cn/photo" alt="春天的山谷 &amp; 河流"><figcaption>摄影：原作者</figcaption></figure><img src="https://mmbiz.qpic.cn/decoration" alt=""><img src="https://mmbiz.qpic.cn/missing"></div>`
	a, err := ParseArticle("https://mp.weixin.qq.com/s/images", source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Preview, "春天的山谷 &amp; 河流") || !strings.Contains(a.Preview, "摄影：原作者") || strings.Contains(a.Preview, "<img") {
		t.Fatalf("preview lost descriptions or loaded images: %s", a.Preview)
	}
	batch := Batch([]Collected{a}, map[string]string{"https://mmbiz.qpic.cn/photo": "/media/1/photo.jpg", "https://mmbiz.qpic.cn/decoration": "/media/2/decoration.jpg", "https://mmbiz.qpic.cn/missing": "/media/3/missing.jpg"}, 1, "作者", "owner")
	images := collectedNodes(t, batch.Items[0].Input.ContentSource, "img")
	if len(images) != 3 {
		t.Fatal("images lost", len(images))
	}
	for i, want := range []string{"春天的山谷 & 河流", "", "采集图片"} {
		if got := collectedAttr(images[i], "alt"); got != want {
			t.Fatalf("image %d alt = %q, want %q", i, got, want)
		}
	}
	if !strings.Contains(batch.Items[0].Input.ContentSource, "摄影：原作者") {
		t.Fatal("caption lost")
	}
}

func TestCollectResolvesArticleLinksWithoutExecutingThem(t *testing.T) {
	source := `<meta property="og:title" content="链接"><base href="https://untrusted-base.test/"><div id="js_content"><p id="section">原文</p><a href="/s/other1?scene=1#part">站内</a><a href="../s/other2">相对</a><a href="//example.org/read?q=1&amp;x=2">省略协议</a><a href="#section">页内</a><a href="https://example.org/absolute">外链</a><a href="https://user:password@example.org/private">凭据</a><a href="javascript:alert(1)">脚本</a><a href="data:text/html,test">数据</a><a href="">空</a><a href="https:">不完整</a></div>`
	a, err := ParseArticle("https://mp.weixin.qq.com/s/links1", source)
	if err != nil {
		t.Fatal(err)
	}
	links := collectedNodes(t, a.Body, "a")
	want := []string{"https://mp.weixin.qq.com/s/other1?scene=1#part", "https://mp.weixin.qq.com/s/other2", "https://example.org/read?q=1&x=2", "https://mp.weixin.qq.com/s/links1#section", "https://example.org/absolute"}
	var got []string
	for _, link := range links {
		if value := collectedAttr(link, "href"); value != "" {
			got = append(got, value)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("links:\n%q\nwant:\n%q", got, want)
	}
	for _, text := range []string{"凭据", "脚本", "数据", "空", "不完整"} {
		if !strings.Contains(a.Body, text) {
			t.Fatalf("unsafe link text lost: %s", text)
		}
	}
	if strings.Contains(a.Body, "untrusted-base") || strings.Contains(a.Body, "user:password") {
		t.Fatal("untrusted base or credentials survived")
	}
	warnings := strings.Join(a.Warnings, "\n")
	if strings.Count(warnings, "部分无效或不安全链接已转为普通文字") != 1 {
		t.Fatal("link cleanup must produce one clear warning", a.Warnings)
	}
}

func TestCollectImageDescriptionEscapingAndBounds(t *testing.T) {
	alt := `&quot;&gt;&lt;svg onload=alert(1)&gt;` + strings.Repeat("图", 300)
	a, err := ParseArticle("https://mp.weixin.qq.com/s/escape", `<meta property="og:title" content="转义"><div id="js_content"><img src="https://mmbiz.qpic.cn/escape" alt="`+alt+`" onerror="evil()"></div>`)
	if err != nil {
		t.Fatal(err)
	}
	body := RewriteImages(a.Body, map[string]string{"https://mmbiz.qpic.cn/escape": "/media/1/safe.jpg"}, false)
	images := collectedNodes(t, body, "img")
	if len(images) != 1 || len([]rune(collectedAttr(images[0], "alt"))) != 250 {
		t.Fatal("description was lost or not bounded", body)
	}
	for _, source := range []string{a.Preview, body} {
		if len(collectedNodes(t, source, "svg")) != 0 || strings.Contains(source, `onerror="`) {
			t.Fatal("description became executable markup", source)
		}
	}
}

func TestCollectPageAnchorsKeepTheFetchedLongLinkIdentity(t *testing.T) {
	address := "https://mp.weixin.qq.com/s?__biz=abc&mid=12&idx=1&sn=xyz"
	a, err := ParseArticle(address, `<meta property="og:title" content="长链接"><meta property="og:url" content="https://mp.weixin.qq.com/s/other3"><div id="js_content"><a href="#chapter">章节</a></div>`)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := ArticleURL(address)
	links := collectedNodes(t, a.Body, "a")
	if len(links) != 1 || collectedAttr(links[0], "href") != canonical+"#chapter" {
		t.Fatal("fragment resolved against metadata rather than fetched document", a.Body)
	}
}

func TestCollectionLinkBoundaries(t *testing.T) {
	base, _ := url.Parse("https://mp.weixin.qq.com/s/bounds")
	for _, input := range []string{"javascript:alert(1)", "file:///etc/passwd", "data:text/html,test", "mailto:private@example.test", "https:", "https:///missing", "https://user:secret@example.test/read", "http://example.test:invalid/read", "/%zz", "/a\nb", strings.Repeat("x", 2049)} {
		if value, ok := collectionLink(base, input); ok {
			t.Errorf("unsafe/incomplete link accepted %q => %q", input, value)
		}
	}
	if value, ok := collectionLink(base, " /s/target#part "); !ok || value != "https://mp.weixin.qq.com/s/target#part" {
		t.Fatal(value, ok)
	}
}

func TestCollectedDescriptionsAndLinksSurviveFileReopenWithoutOverwritingEdits(t *testing.T) {
	a, err := ParseArticle("https://mp.weixin.qq.com/s/stored", `<meta property="og:title" content="保存图文"><div id="js_content"><p><a href="/s/original">延伸阅读</a></p><img src="https://mmbiz.qpic.cn/stored" alt="有含义的替代文本"></div>`)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "content.json")
	repo, err := content.NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	batch := Batch([]Collected{a}, map[string]string{"https://mmbiz.qpic.cn/stored": "/media/1/stored.jpg"}, 1, "本地作者", "owner")
	plan, err := repo.PreviewImport(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := repo.ApplyImport(t.Context(), batch, plan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	repo, err = content.NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	post, err := repo.GetByID(t.Context(), applied.Items[0].PostID)
	if err != nil || !strings.Contains(post.ContentHTML, `alt="有含义的替代文本"`) || !strings.Contains(post.ContentHTML, `href="https://mp.weixin.qq.com/s/original"`) {
		t.Fatal(post, err)
	}
	updated, err := repo.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: "人工修改", Slug: post.Slug, ContentSource: "<p>本地新正文</p>", AuthorID: 1})
	if err != nil {
		t.Fatal(err)
	}
	a.Body = strings.ReplaceAll(a.Body, "有含义的替代文本", "来源图片已更新")
	batch = Batch([]Collected{a}, nil, 1, "本地作者", "owner")
	plan, err = repo.PreviewImport(t.Context(), batch)
	if err != nil || plan.New != 0 || plan.Existing != 1 || plan.Items[0].Action != "changed" {
		t.Fatal(plan, err)
	}
	if _, err = repo.ApplyImport(t.Context(), batch, plan.Revision); err != nil {
		t.Fatal(err)
	}
	unchanged, err := repo.GetByID(t.Context(), post.ID)
	if err != nil || unchanged.Version != updated.Version || unchanged.Title != "人工修改" || unchanged.ContentSource != "<p>本地新正文</p>" {
		t.Fatal("recollection overwrote user edit", unchanged, err)
	}
}
