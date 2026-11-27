package contentimport

import (
	"errors"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

func TestWXRRejectsAmbiguousDocumentStructure(t *testing.T) {
	withoutDeclaration := strings.TrimPrefix(sampleWXR, `<?xml version="1.0" encoding="UTF-8"?>`)
	firstItemStart := strings.Index(sampleWXR, "<item>")
	firstItemEnd := strings.Index(sampleWXR, "</item>") + len("</item>")
	firstItem := sampleWXR[firstItemStart:firstItemEnd]
	tests := []struct{ name, source string }{
		{"multiple roots", sampleWXR + withoutDeclaration},
		{"leading text", "unexpected data\n" + withoutDeclaration},
		{"trailing text", sampleWXR + "\nunexpected data"},
		{"multiple channels", strings.Replace(sampleWXR, "</channel>", "</channel><channel><wp:wxr_version>1.2</wp:wxr_version></channel>", 1)},
		{"duplicate version", strings.Replace(sampleWXR, "<wp:wxr_version>1.2</wp:wxr_version>", "<wp:wxr_version>1.1</wp:wxr_version><wp:wxr_version>1.2</wp:wxr_version>", 1)},
		{"foreign RSS namespace", strings.Replace(sampleWXR, `<rss version="2.0"`, `<rss xmlns="urn:other" version="2.0"`, 1)},
		{"foreign channel namespace", strings.Replace(strings.Replace(sampleWXR, "<channel>", `<x:channel xmlns:x="urn:other">`, 1), "</channel>", "</x:channel>", 1)},
		{"foreign item namespace", strings.Replace(strings.Replace(sampleWXR, "<item>", `<x:item xmlns:x="urn:other">`, 1), "</item>", "</x:item>", 1)},
		{"item outside channel", strings.Replace(sampleWXR, "</channel>", "</channel>"+firstItem, 1)},
		{"wrapped item", strings.Replace(sampleWXR, firstItem, "<wrapper>"+firstItem+"</wrapper>", 1)},
		{"foreign title shadow", strings.Replace(sampleWXR, "<title>原站文章</title>", `<title>原站文章</title><x:title xmlns:x="urn:other">Different title</x:title>`, 1)},
		{"foreign link shadow", strings.Replace(sampleWXR, "</link>", `</link><x:link xmlns:x="urn:other">https://other.test/wrong/</x:link>`, 1)},
		{"foreign category", strings.Replace(sampleWXR, `<category domain="category">设计</category>`, `<x:category xmlns:x="urn:other" domain="category">设计</x:category>`, 1)},
		{"duplicate attributes", strings.Replace(sampleWXR, `<rss version="2.0"`, `<rss version="2.0" version="2.0"`, 1)},
		{"nested declaration", strings.Replace(sampleWXR, "<channel>", `<channel><?xml version="1.0"?>`, 1)},
		{"trailing declaration", sampleWXR + `<?xml version="1.0"?>`},
		{"duplicate declaration", `<?xml version="1.0"?>` + sampleWXR},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc, err := Parse(Request{Source: "old-site", Format: "wordpress", State: "preserve", Files: []File{
				{Name: "export.xml", Text: test.source},
			}}, time.Now())
			var invalid *content.ValidationError
			if !errors.As(err, &invalid) {
				t.Errorf("expected a validation error, got %v", err)
			}
			if len(doc.Items) != 0 || len(doc.Batch.Items) != 0 {
				t.Errorf("ambiguous XML yielded a partial import: %d preview items, %d batch items", len(doc.Items), len(doc.Batch.Items))
			}
		})
	}
}

func TestWXRValidDocumentStructurePreservesContent(t *testing.T) {
	withoutDeclaration := strings.TrimPrefix(sampleWXR, `<?xml version="1.0" encoding="UTF-8"?>`)
	prefixAlias := strings.ReplaceAll(sampleWXR, "wp:", "wordpress:")
	prefixAlias = strings.Replace(prefixAlias, "xmlns:wp=", "xmlns:wordpress=", 1)
	for _, test := range []struct{ name, source string }{
		{"standard export", sampleWXR},
		{"UTF-8 BOM", "\ufeff" + sampleWXR},
		{"no declaration", withoutDeclaration},
		{"surrounding comments", " \n<!-- before -->\n" + withoutDeclaration + "\n<!-- after -->\r\n"},
		{"namespace prefix alias", prefixAlias},
		{"unknown extension metadata", strings.Replace(sampleWXR, "<channel>", `<channel><x:metadata xmlns:x="urn:extension"><x:channel>extra</x:channel></x:metadata>`, 1)},
		{"repeated comments and metadata", strings.Replace(sampleWXR, "</item>", `<wp:postmeta><wp:meta_key>first</wp:meta_key><wp:meta_value>one</wp:meta_value></wp:postmeta><wp:postmeta><wp:meta_key>second</wp:meta_key><wp:meta_value>two</wp:meta_value></wp:postmeta><wp:comment><wp:comment_id>2</wp:comment_id></wp:comment></item>`, 1)},
		{"foreign repeatable extension fields", strings.Replace(sampleWXR, "</item>", `<x:postmeta xmlns:x="urn:extension">one</x:postmeta><x:postmeta xmlns:x="urn:extension">two</x:postmeta><x:comment xmlns:x="urn:extension">extra</x:comment><x:comment xmlns:x="urn:extension">more</x:comment></item>`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc, err := Parse(Request{Source: "old-site", Format: "wordpress", State: "preserve", Files: []File{
				{Name: "export.xml", Text: test.source},
			}}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Items) != 2 || len(doc.Skipped) != 1 || doc.Batch.Items[0].Input.Title != "原站文章" || doc.Batch.Items[1].Input.Status != content.StatusPrivate {
				t.Fatalf("export content or private status changed: %+v", doc.Items)
			}
			if !strings.Contains(doc.Items[0].Preview, `href="/pages/about"`) {
				t.Fatalf("content link changed: %s", doc.Items[0].Preview)
			}
		})
	}
}
