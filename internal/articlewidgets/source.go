package articlewidgets

import (
	htmlparser "golang.org/x/net/html"
	"strconv"
	"strings"
)

type Reference struct {
	Start, End int
	Config     Config
	Valid      bool
}

// References preserves surrounding HTML and treats each widget as an opaque
// subtree. Nested fallbacks cannot create extra interactive identities.
func References(source string) []Reference {
	if !strings.Contains(source, "data-np-config") {
		return nil
	}
	t := htmlparser.NewTokenizer(strings.NewReader(source))
	offset, depth := 0, 0
	var refs []Reference
	var current Reference
	for k := t.Next(); k != htmlparser.ErrorToken; k = t.Next() {
		start := offset
		offset += len(t.Raw())
		if k != htmlparser.StartTagToken && k != htmlparser.EndTagToken && k != htmlparser.SelfClosingTagToken {
			continue
		}
		n := t.Token()
		if n.Data != "aside" {
			continue
		}
		if depth > 0 {
			if k == htmlparser.EndTagToken {
				depth--
				if depth == 0 {
					current.End = offset
					refs = append(refs, current)
				}
			} else {
				depth++
			}
			continue
		}
		if k == htmlparser.EndTagToken {
			continue
		}
		kind, encoded := "", ""
		for _, a := range n.Attr {
			if a.Key == "data-np-widget" {
				kind = a.Val
			}
			if a.Key == "data-np-config" {
				encoded = a.Val
			}
		}
		if encoded == "" || kind == "post" {
			continue
		}
		c, err := Decode(encoded)
		current = Reference{Start: start, Config: c, Valid: err == nil && c.Kind == kind}
		depth = 1
	}
	if depth > 0 {
		current.End = len(source)
		refs = append(refs, current)
	}
	return refs
}

func Expand(source string, postID int64, skin string) string {
	return ExpandWithSender(source, postID, skin, "", "")
}

func ExpandWithSender(source string, postID int64, skin, sender, avatar string) string {
	return ExpandForTarget(source, strconv.FormatInt(postID, 10), skin, sender, avatar)
}

func ExpandForTarget(source, target, skin, sender, avatar string) string {
	refs := References(source)
	if len(refs) == 0 {
		return source
	}
	var out strings.Builder
	start := 0
	budget := 4 << 20
	seen := map[string]bool{}
	for i, ref := range refs {
		out.WriteString(source[start:ref.Start])
		replacement := `<aside><p>组件暂不可用，请检查配置或重复的组件编号。</p></aside>`
		if ref.Valid && i < 200 && !seen[ref.Config.ID] {
			replacement = RenderForTarget(ref.Config, target, skin, sender, avatar)
			seen[ref.Config.ID] = true
		}
		if len(replacement) > budget {
			replacement = `<p>本页组件内容已达到展示上限。</p>`
		}
		if len(replacement) > budget || i >= 200 {
			replacement = ""
		}
		budget -= len(replacement)
		out.WriteString(replacement)
		start = ref.End
	}
	out.WriteString(source[start:])
	return out.String()
}

func Find(source, id string) (Config, bool) {
	var result Config
	count := 0
	for i, r := range References(source) {
		if i >= 200 {
			break
		}
		if r.Valid && r.Config.ID == id {
			result = r.Config
			count++
		}
	}
	return result, count == 1
}
