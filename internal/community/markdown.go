package community

import (
	"bytes"
	"html"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))
var markdownPolicy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowRelativeURLs(true)
	p.RequireNoReferrerOnLinks(true)
	return p
}()

// RenderBody never permits raw HTML or remote images. Markdown images must be
// bound attachments and use the authenticated circle download endpoint.
func RenderBody(slug, source, format string, files []Attachment) string {
	if format != "markdown" {
		return strings.ReplaceAll(html.EscapeString(source), "\n", "<br>\n")
	}
	input := []byte(source)
	doc := markdown.Parser().Parse(text.NewReader(input))
	allowed := map[string]bool{}
	for _, f := range files {
		if strings.HasPrefix(f.MIME, "image/") {
			allowed[f.ID] = true
		}
	}
	remove := []ast.Node{}
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if img, ok := node.(*ast.Image); ok {
			dest := string(img.Destination)
			id := strings.TrimPrefix(dest, "attachment:")
			if !strings.HasPrefix(dest, "attachment:") || !allowed[id] {
				remove = append(remove, img)
			} else {
				img.Destination = []byte("/api/v1/community/circles/" + slug + "/attachments/" + id + "?inline=1")
			}
		}
		return ast.WalkContinue, nil
	})
	for _, node := range remove {
		node.Parent().RemoveChild(node.Parent(), node)
	}
	var out bytes.Buffer
	if err := markdown.Renderer().Render(&out, input, doc); err != nil {
		return html.EscapeString(source)
	}
	return markdownPolicy.Sanitize(out.String())
}
