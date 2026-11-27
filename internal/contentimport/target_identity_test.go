package contentimport

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/content"
)

func targetIdentityRequest(format string, batch bool) Request {
	r := Request{Source: "identity-notes", Format: format, State: "draft", Media: map[string]string{
		"https://old.test/cover.jpg":                "/media/1/cover.jpg",
		"https://old.test/unreferenced-private.png": "/media/9/private.png",
	}}
	if format == "markdown" {
		old := File{Name: "old.md", Text: "---\ntitle: Old source\nslug: shared\n---\nOriginal body"}
		r.Files = []File{old}
		if batch {
			r.Files = []File{
				{Name: "index.md", Text: "---\nslug: index\n---\n[Old](./old.md?ref=old#one)\n\n[New](./new.md?ref=new#two)\n\n[Direct](/posts/shared?ref=direct#three)\n\n![Cover](https://old.test/cover.jpg)"},
				old,
				{Name: "new.md", Text: "---\ntitle: New source\nslug: shared\n---\nNew body"},
			}
		}
		return r
	}
	item := func(id int, title, slug, sourcePath, body string) string {
		return fmt.Sprintf(`<item><title>%s</title><link>https://old.test/%s/</link><wp:post_id>%d</wp:post_id><wp:post_type>post</wp:post_type><wp:post_name>%s</wp:post_name><wp:status>draft</wp:status><content:encoded><![CDATA[%s]]></content:encoded></item>`, title, sourcePath, id, slug, body)
	}
	old := item(1, "Old source", "shared", "legacy-old", "<p>Original body</p>")
	items := old
	if batch {
		items = item(3, "Index", "index", "legacy-index", `<p><a href="https://old.test/legacy-old/#one">Old</a><a href="https://old.test/legacy-new/#two">New</a><a href="/posts/shared?ref=direct#three">Direct</a></p><img src="https://old.test/cover.jpg" alt="Cover"><script>PRIVATE_SOURCE_SENTINEL</script>`) + old + item(2, "New source", "shared", "legacy-new", "<p>New body</p>")
	}
	r.Files = []File{{Name: "export.xml", Text: `<rss xmlns:wp="http://wordpress.org/export/1.2/" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><wp:wxr_version>1.2</wp:wxr_version>` + items + `</channel></rss>`}}
	return r
}

func TestImportLinksKeepSourceIdentityWhenSlugsAreReused(t *testing.T) {
	for _, format := range []string{"markdown", "wordpress"} {
		for _, mode := range []string{"memory", "file"} {
			t.Run(format+"/"+mode, func(t *testing.T) {
				var repo content.Repository = content.NewMemoryRepository(false)
				path := filepath.Join(t.TempDir(), "content.json")
				var err error
				if mode == "file" {
					repo, err = content.NewFileRepository(path, false)
					if err != nil {
						t.Fatal(err)
					}
				}
				ctx := t.Context()
				parse := func(r Request) Document {
					t.Helper()
					doc, err := Parse(r, time.Now())
					if err != nil {
						t.Fatal(err)
					}
					return doc
				}
				preview := func(doc Document) content.ImportPlan {
					t.Helper()
					plan, err := repo.PreviewImport(ctx, doc.Batch)
					if err != nil || plan.Conflicts != 0 {
						t.Fatalf("preview: %+v, %v", plan, err)
					}
					return plan
				}
				first := parse(targetIdentityRequest(format, false))
				plan := preview(first)
				plan, err = repo.ApplyImport(ctx, first.Batch, plan.Revision)
				if err != nil {
					t.Fatal(err)
				}
				oldID := plan.Items[0].PostID
				if _, err := repo.Update(ctx, oldID, content.Mutation{ExpectedVersion: plan.Items[0].Version}, content.PostInput{
					Title: "Manually edited old content", Slug: "renamed", Status: content.StatusDraft, ContentSource: "<p>Saved edit</p>",
				}); err != nil {
					t.Fatal(err)
				}

				request := targetIdentityRequest(format, true)
				doc := parse(request)
				initial := preview(doc)
				if initial.New != 2 || initial.Existing != 1 {
					t.Fatalf("the released slug should be reusable: %+v", initial)
				}
				request.Media["https://old.test/cover.jpg"] = "/media/2/changed.jpg"
				doc.Batch.Items[0].Input.AuthorID = 7
				doc.Batch.Items[0].Input.AuthorName = "Mapped author"
				doc.Batch.Items[0].Input.Actor = "import-operator"
				AlignTargets(&doc, initial, time.Now())
				if input := doc.Batch.Items[0].Input; input.AuthorID != 7 || input.AuthorName != "Mapped author" || input.Actor != "import-operator" {
					t.Fatalf("alignment lost assigned author or actor: %+v", input)
				}
				want := map[string]string{"Old": "/posts/renamed#one", "New": "/posts/shared#two", "Direct": "https://old.test/posts/shared?ref=direct#three"}
				if format == "markdown" {
					want = map[string]string{"Old": "/posts/renamed?ref=old#one", "New": "/posts/shared?ref=new#two", "Direct": "/posts/shared?ref=direct#three"}
				}
				check := func(body string) {
					t.Helper()
					got := targetIdentityLinks(body)
					for label, href := range want {
						if got[label] != href {
							t.Errorf("%s link = %q, want %q", label, got[label], href)
						}
					}
					if !strings.Contains(body, `src="/media/1/cover.jpg"`) {
						t.Error("target alignment changed the already resolved image mapping")
					}
				}
				check(doc.Items[0].Preview)
				aligned := doc.Items[0].Preview
				AlignTargets(&doc, initial, time.Now())
				if doc.Items[0].Preview != aligned {
					t.Fatal("repeating target alignment changed the preview")
				}
				encoded, err := json.Marshal(doc)
				if err != nil || strings.Contains(string(encoded), "PRIVATE_SOURCE_SENTINEL") || strings.Contains(string(encoded), "unreferenced-private") {
					t.Fatalf("preview serialization exposed raw input: %v", err)
				}
				plan = preview(doc)
				plan, err = repo.ApplyImport(ctx, doc.Batch, plan.Revision)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "file" {
					repo, err = content.NewFileRepository(path, false)
					if err != nil {
						t.Fatal(err)
					}
				}
				post, err := repo.GetByID(ctx, plan.Items[0].PostID)
				if err != nil {
					t.Fatal(err)
				}
				check(post.ContentHTML)
				old, err := repo.GetByID(ctx, oldID)
				if err != nil || old.Slug != "renamed" || old.Title != "Manually edited old content" || !strings.Contains(old.ContentHTML, "Saved edit") {
					t.Fatalf("reimport overwrote the existing target: %+v, %v", old, err)
				}
			})
		}
	}
}

func TestAlignTargetsSeparatesExistingSourcesWithSameSlug(t *testing.T) {
	for _, format := range []string{"markdown", "wordpress"} {
		t.Run(format, func(t *testing.T) {
			doc, err := Parse(targetIdentityRequest(format, true), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			plan := content.ImportPlan{Items: []content.ImportRow{
				{Action: "create", URL: "/posts/index"},
				{Action: "skip", PostID: 10, URL: "/posts/renamed-old"},
				{Action: "changed", PostID: 11, URL: "/posts/renamed-new"},
			}}
			AlignTargets(&doc, plan, time.Now())
			want := map[string]string{"Old": "/posts/renamed-old#one", "New": "/posts/renamed-new#two", "Direct": "https://old.test/posts/shared?ref=direct#three"}
			if format == "markdown" {
				want = map[string]string{"Old": "/posts/renamed-old?ref=old#one", "New": "/posts/renamed-new?ref=new#two", "Direct": "/posts/shared?ref=direct#three"}
			}
			links := targetIdentityLinks(doc.Items[0].Preview)
			for label, href := range want {
				if links[label] != href {
					t.Errorf("%s link = %q, want %q", label, links[label], href)
				}
			}
		})
	}
}

func targetIdentityLinks(source string) map[string]string {
	links := map[string]string{}
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return links
	}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" && node.FirstChild != nil {
			for _, attr := range node.Attr {
				if attr.Key == "href" {
					links[node.FirstChild.Data] = attr.Val
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return links
}
