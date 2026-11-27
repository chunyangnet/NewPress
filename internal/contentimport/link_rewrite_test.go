package contentimport

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/content"
)

func TestMarkdownLinksResolveEncodedFilenames(t *testing.T) {
	tests := []struct {
		name, filename, link, want string
		decoy                      string
	}{
		{name: "ASCII", filename: "next.md", link: "./next.md#chapter", want: "/posts/destination#chapter"},
		{name: "Unicode", filename: "下一篇.md", link: "./下一篇.md#details", want: "/posts/destination#details"},
		{name: "lowercase escapes", filename: "下一篇.md", link: "./%e4%b8%8b%e4%b8%80%e7%af%87.md", want: "/posts/destination"},
		{name: "spaces", filename: "my notes.md", link: "./my notes.md#chapter", want: "/posts/destination#chapter"},
		{name: "escaped spaces", filename: "my notes.md", link: "./my%20notes.md", want: "/posts/destination"},
		{name: "literal percent", filename: "my%20notes.md", link: "./my%2520notes.md", want: "/posts/destination"},
		{name: "encoded filename collision", filename: "my notes.md", decoy: "my%20notes.md", link: "./my%20notes.md", want: "/posts/destination"},
		{name: "no double decoding", filename: "my%20notes.md", decoy: "my notes.md", link: "./my%2520notes.md", want: "/posts/destination"},
		{name: "unmapped encoded filename", filename: "my%20notes.md", link: "./my%20notes.md", want: "./my%20notes.md"},
		{name: "query and fragment", filename: "next.md", link: "./next.md?view=compact&lang=zh#details", want: "/posts/destination?view=compact&lang=zh#details"},
		{name: "empty query", filename: "next.md", link: "./next.md?#details", want: "/posts/destination?#details"},
		{name: "fragment in filename", filename: "notes#1.md", link: "./notes%231.md#details", want: "/posts/destination#details"},
		{name: "query in filename", filename: "notes?.md", link: "./notes%3F.md?view=compact", want: "/posts/destination?view=compact"},
		{name: "external URL", filename: "next.md", link: "https://other.test/next.md#chapter", want: "https://other.test/next.md#chapter"},
		{name: "protocol relative URL", filename: "next.md", link: "//other.test/next.md", want: "//other.test/next.md"},
		{name: "same page anchor", filename: "next.md", link: "#chapter", want: "#chapter"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := Request{Source: "notes", Format: "markdown", State: "draft", Files: []File{
				{Name: "index.md", Text: "---\nslug: index\n---\n[next](<" + test.link + ">)"},
				{Name: test.filename, Text: "---\nslug: destination\n---\nDestination"},
			}}
			if test.decoy != "" {
				r.Files = append(r.Files, File{Name: test.decoy, Text: "---\nslug: other-destination\n---\nOther destination"})
			}
			doc, err := Parse(r, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if want := `href="` + html.EscapeString(test.want) + `"`; !strings.Contains(doc.Items[0].Preview, want) {
				t.Fatalf("want %s in imported preview, got %s", want, doc.Items[0].Preview)
			}
		})
	}
}

func TestMarkdownEncodedLinksFollowRenamedImportedTargets(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
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
			importFiles := func(files []File) content.ImportPlan {
				t.Helper()
				doc, err := Parse(Request{Source: "notes", Format: "markdown", State: "draft", Files: files}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				initial, err := repo.PreviewImport(ctx, doc.Batch)
				if err != nil {
					t.Fatal(err)
				}
				AlignTargets(&doc, initial, time.Now())
				plan, err := repo.PreviewImport(ctx, doc.Batch)
				if err != nil || plan.Conflicts != 0 {
					t.Fatalf("preview: %+v, %v", plan, err)
				}
				plan, err = repo.ApplyImport(ctx, doc.Batch, plan.Revision)
				if err != nil {
					t.Fatal(err)
				}
				return plan
			}
			target := File{Name: "下一篇.md", Text: "---\nslug: destination\n---\nDestination"}
			first := importFiles([]File{target})
			if _, err := repo.Update(ctx, first.Items[0].PostID, content.Mutation{ExpectedVersion: first.Items[0].Version}, content.PostInput{
				Title: "Renamed destination", Slug: "renamed", Status: content.StatusDraft,
			}); err != nil {
				t.Fatal(err)
			}
			plan := importFiles([]File{
				{Name: "index.md", Text: "---\nslug: index\n---\n[next](<./下一篇.md?view=compact#details>)"}, target,
			})
			if plan.Items[0].Action != "created" || plan.Items[1].Action != "skip" {
				t.Fatalf("unexpected import actions: %+v", plan.Items)
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
			const want = `href="/posts/renamed?view=compact#details"`
			if !strings.Contains(post.ContentHTML, want) || !strings.Contains(post.ContentSource, want) {
				t.Fatalf("saved link did not follow the renamed target: %s", post.ContentHTML)
			}
		})
	}
}

func TestWXRLinksKeepQueryBasedSourceIdentity(t *testing.T) {
	source := strings.Replace(sampleWXR, "https://old.test/about/", "https://old.test/?page_id=11", -1)
	source = strings.Replace(source, `<a href="https://old.test/?page_id=11">About</a>`,
		`<a href="https://old.test/?page_id=11#details">About</a><a href="https://old.test/?page_id=12#details">Other</a>`, 1)
	doc, err := Parse(Request{Source: "old-site", Format: "wordpress", State: "preserve", Files: []File{
		{Name: "export.xml", Text: source},
	}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`href="/pages/about#details"`, `href="https://old.test/?page_id=12#details"`} {
		if !strings.Contains(doc.Items[0].Preview, want) {
			t.Fatalf("want %s in WordPress preview, got %s", want, doc.Items[0].Preview)
		}
	}
}
