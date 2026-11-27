package contenthtml

import (
	"bytes"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

type ResponsiveImage struct {
	Src, SrcSet   string
	Width, Height int
}

// WithResponsiveImages is a sanitizer projection: only sanitized source tokens
// plus escaped, server-supplied local media attributes reach the output. Stored
// content and revision source are never rewritten for display optimization.
func WithResponsiveImages(source string, images map[string]ResponsiveImage) string {
	source = Sanitize(source)
	if len(images) == 0 {
		return source
	}
	tokens := html.NewTokenizer(strings.NewReader(source))
	var out bytes.Buffer
	for kind := tokens.Next(); kind != html.ErrorToken; kind = tokens.Next() {
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			out.Write(tokens.Raw())
			continue
		}
		token := tokens.Token()
		if token.Data != "img" {
			out.WriteString(token.String())
			continue
		}
		var view ResponsiveImage
		for _, attr := range token.Attr {
			if attr.Key == "src" {
				view = images[attr.Val]
			}
		}
		parsed, err := url.Parse(view.Src)
		if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/media/") || strings.ContainsAny(view.Src, "\r\n\\") || view.Width <= 0 || view.Height <= 0 {
			out.WriteString(token.String())
			continue
		}
		attrs := []html.Attribute{}
		for _, attr := range token.Attr {
			if attr.Key != "src" && attr.Key != "srcset" && attr.Key != "sizes" && attr.Key != "width" && attr.Key != "height" {
				attrs = append(attrs, attr)
			}
		}
		attrs = append(attrs, html.Attribute{Key: "src", Val: view.Src}, html.Attribute{Key: "srcset", Val: view.SrcSet}, html.Attribute{Key: "sizes", Val: "(max-width: 800px) 100vw, 800px"}, html.Attribute{Key: "width", Val: strconv.Itoa(view.Width)}, html.Attribute{Key: "height", Val: strconv.Itoa(view.Height)})
		token.Attr = attrs
		out.WriteString(token.String())
	}
	return out.String()
}
