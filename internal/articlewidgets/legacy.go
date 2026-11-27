package articlewidgets

import (
	"fmt"
	"strings"

	htmlparser "golang.org/x/net/html"
)

// EnhanceCode upgrades existing plain editor code blocks on Chunnel without
// changing saved source. Rich or malformed preformatted HTML stays untouched.
func EnhanceCode(source string, post int64) string {
	if !strings.Contains(source, "<pre") {
		return source
	}
	t := htmlparser.NewTokenizer(strings.NewReader(source))
	offset, start, depth, widgets, index := 0, -1, 0, 0, 0
	var out strings.Builder
	last := 0
	for k := t.Next(); k != htmlparser.ErrorToken; k = t.Next() {
		at := offset
		offset += len(t.Raw())
		if k != htmlparser.StartTagToken && k != htmlparser.EndTagToken {
			continue
		}
		n := t.Token()
		if n.Data == "aside" {
			if k == htmlparser.EndTagToken {
				if widgets > 0 {
					widgets--
				}
			} else {
				if widgets > 0 {
					widgets++
				} else {
					for _, a := range n.Attr {
						if a.Key == "data-np-widget" {
							widgets = 1
						}
					}
				}
			}
		}
		if n.Data != "pre" || widgets > 0 {
			continue
		}
		if k == htmlparser.StartTagToken {
			if depth == 0 {
				start = at
			}
			depth++
			continue
		}
		if depth == 0 {
			continue
		}
		depth--
		if depth != 0 || start < 0 {
			continue
		}
		raw := source[start:offset]
		if len(raw) > 64<<10 || index >= 50 {
			continue
		}
		fragment, err := htmlparser.Parse(strings.NewReader(raw))
		if err != nil {
			continue
		}
		var code *htmlparser.Node
		var walk func(*htmlparser.Node)
		walk = func(n *htmlparser.Node) {
			if n.Type == htmlparser.ElementNode && n.Data == "code" {
				code = n
			}
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
		}
		walk(fragment)
		if code == nil || code.Parent == nil || code.Parent.Data != "pre" {
			continue
		}
		var plain strings.Builder
		valid := true
		for sibling := code.Parent.FirstChild; sibling != nil; sibling = sibling.NextSibling {
			if sibling != code && (sibling.Type != htmlparser.TextNode || strings.TrimSpace(sibling.Data) != "") {
				valid = false
			}
		}
		for child := code.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != htmlparser.TextNode {
				valid = false
				break
			}
			plain.WriteString(child.Data)
		}
		if !valid {
			continue
		}
		language := "auto"
		for _, a := range code.Attr {
			if a.Key == "class" && strings.HasPrefix(a.Val, "language-") {
				language = strings.TrimPrefix(a.Val, "language-")
			}
		}
		c := Config{Version: 1, ID: fmt.Sprintf("legacy_code_%d_%d", post, index), Kind: "code", Text: plain.String(), Language: language}
		if c.Validate() != nil {
			continue
		}
		index++
		out.WriteString(source[last:start])
		out.WriteString(Render(c, post, "chunnel"))
		last = offset
	}
	if last == 0 {
		return source
	}
	out.WriteString(source[last:])
	return out.String()
}
