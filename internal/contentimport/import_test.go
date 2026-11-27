package contentimport

import (
	"newpress.local/newpress/internal/content"
	"strings"
	"testing"
	"time"
)

func TestMarkdownConversionAndSafety(t *testing.T) {
	r := Request{Source: "notes", Format: "markdown", State: "draft", Files: []File{{Name: "hello.md", Text: "---\ntitle: 中文文章\nauthor: Alice\ndate: 2020-01-02\nstatus: publish\ncategories: [设计, 笔记]\ntags: [Go]\npermalink: /2020/hello/\n---\n# 你好\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n<script>alert(1)</script>\n\n![photo](https://old.test/photo.jpg)\n\n[bad](javascript:alert(1))"}}}
	d, err := Parse(r, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p := d.Batch.Items[0]
	if p.Input.Status != "draft" || p.OldPath != "/2020/hello/" || len(p.Categories) != 2 || !strings.Contains(p.Input.ContentHTML, "<table>") || strings.Contains(p.Input.ContentHTML, "<script") || strings.Contains(p.Input.ContentHTML, "javascript:") || strings.Contains(p.Input.ContentHTML, "<img") {
		t.Fatal(p)
	}
	if len(d.Items[0].Images) != 1 || d.Authors[0] != "Alice" {
		t.Fatal(d)
	}
	r.Media = map[string]string{"https://old.test/photo.jpg": "/media/1/photo.jpg"}
	d, err = Parse(r, time.Now())
	if err != nil || !strings.Contains(d.Items[0].Preview, `src="/media/1/photo.jpg"`) {
		t.Fatal(d, err)
	}
	for _, body := range []string{"---\ntitle: a\ntitle: b\n---\nbody", "---\ntitle: &x hello\nauthor: *x\n---\nbody", "---\ncategories: [[a]]\n---\nbody", "---\ntitle: ok", "---\ndate: tomorrow\n---\nbody"} {
		r.Files[0].Text = body
		if _, err = Parse(r, time.Now()); err == nil {
			t.Fatal("accepted invalid front matter", body)
		}
	}
}

func TestImportLinksFollowExistingRenamedTargets(t *testing.T) {
	r := Request{Source: "notes", Format: "markdown", State: "draft", Files: []File{{Name: "a.md", Text: "---\nslug: first\n---\n[second](./b.md#chapter)"}, {Name: "b.md", Text: "---\nslug: second\n---\n## Chapter"}}}
	d, err := Parse(r, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Items[0].Preview, `href="/posts/second#chapter"`) {
		t.Fatal(d.Items[0].Preview)
	}
	plan := content.ImportPlan{Items: []content.ImportRow{{Action: "create", URL: "/posts/first"}, {Action: "skip", PostID: 9, URL: "/posts/renamed"}}}
	AlignTargets(&d, plan, time.Now())
	if !strings.Contains(d.Batch.Items[0].Input.ContentHTML, `href="/posts/renamed#chapter"`) {
		t.Fatal(d.Batch.Items[0].Input.ContentHTML)
	}
}

const sampleWXR = `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:wp="http://wordpress.org/export/1.2/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><wp:wxr_version>1.2</wp:wxr_version><item><title>原站文章</title><link>https://old.test/2020/first/</link><dc:creator>alice</dc:creator><wp:post_id>10</wp:post_id><wp:post_type>post</wp:post_type><wp:status>publish</wp:status><wp:post_date_gmt>2020-01-02 12:00:00</wp:post_date_gmt><wp:post_name>first</wp:post_name><content:encoded><![CDATA[<p><a href="https://old.test/about/">About</a></p><img src="/uploads/p.png" onerror="alert(1)"><script>bad()</script>]]></content:encoded><category domain="category">设计</category><category domain="post_tag">笔记</category><wp:comment><wp:comment_id>1</wp:comment_id></wp:comment></item><item><title>私密页</title><link>https://old.test/about/</link><wp:post_id>11</wp:post_id><wp:post_type>page</wp:post_type><wp:post_name>about</wp:post_name><wp:status>publish</wp:status><wp:post_password>DO-NOT-EXPORT</wp:post_password><content:encoded>private body</content:encoded></item><item><wp:post_id>12</wp:post_id><wp:post_type>attachment</wp:post_type><wp:attachment_url>https://old.test/uploads/p.png</wp:attachment_url></item></channel></rss>`

func TestWXRPrivacyLinksAndHostileXML(t *testing.T) {
	r := Request{Source: "old-site", Format: "wordpress", State: "preserve", Files: []File{{Name: "export.xml", Text: sampleWXR}}}
	d, err := Parse(r, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 2 || len(d.Skipped) != 1 || d.Batch.Items[1].Input.Status != "private" || d.Batch.Items[0].OldPath != "/2020/first/" {
		t.Fatal(d)
	}
	if strings.Contains(strings.Join(d.Items[1].Warnings, " "), "改为草稿") {
		t.Fatal("private status warning contradicts saved status")
	}
	html := d.Items[0].Preview
	if !strings.Contains(html, `href="/pages/about"`) || strings.Contains(html, "<img") || strings.Contains(html, "script") || !strings.Contains(html, "https://old.test/uploads/p.png") {
		t.Fatal(html)
	}
	for _, bad := range []string{strings.Replace(sampleWXR, "<rss", `<!DOCTYPE rss [<!ENTITY a SYSTEM "file:///etc/passwd">]><rss`, 1), strings.Replace(sampleWXR, "<wp:post_id>10</wp:post_id>", "<wp:post_id>10</wp:post_id><wp:post_id>20</wp:post_id>", 1), strings.Replace(sampleWXR, "<title>原站文章</title>", "<title>&unknown;</title>", 1), strings.Replace(sampleWXR, "<wp:wxr_version>1.2</wp:wxr_version>", "", 1)} {
		r.Files[0].Text = bad
		if _, err = Parse(r, time.Now()); err == nil {
			t.Fatal("accepted hostile XML")
		}
	}
}
