package contenthtml

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// SnippetKey pins a reference to an immutable body revision, never to latest.
type SnippetKey struct {
	ID      int64 `json:"id"`
	Version int64 `json:"version"`
}

type SnippetReference struct {
	SnippetKey
	Start, End int
}

func SnippetShell(key SnippetKey) string {
	return fmt.Sprintf(`<aside data-np-snippet="%d" data-np-snippet-version="%d"></aside>`, key.ID, key.Version)
}

// SnippetReferences owns the entire aside subtree, including malformed or
// nested fallback content. A self-closing aside is not void in HTML and thus
// also consumes through its closing tag (or EOF). Unrelated bytes are retained.
func SnippetReferences(source string) []SnippetReference {
	if !strings.Contains(strings.ToLower(source), "data-np-snippet") {
		return nil
	}
	tokens := html.NewTokenizer(strings.NewReader(source))
	var refs []SnippetReference
	var current SnippetReference
	offset, depth := 0, 0
	for kind := tokens.Next(); kind != html.ErrorToken; kind = tokens.Next() {
		start := offset
		offset += len(tokens.Raw())
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken && kind != html.EndTagToken {
			continue
		}
		token := tokens.Token()
		if token.Data != "aside" {
			continue
		}
		if depth > 0 {
			if kind == html.EndTagToken {
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
		if kind == html.EndTagToken {
			continue
		}
		values := map[string]string{}
		duplicate, marked := false, false
		for _, attr := range token.Attr {
			if attr.Key != "data-np-snippet" && attr.Key != "data-np-snippet-version" {
				continue
			}
			marked = true
			if _, exists := values[attr.Key]; exists {
				duplicate = true
			}
			values[attr.Key] = attr.Val
		}
		if !marked {
			continue
		}
		current = SnippetReference{Start: start}
		if !duplicate {
			current.ID = snippetNumber(values["data-np-snippet"])
			current.Version = snippetNumber(values["data-np-snippet-version"])
		}
		depth = 1
	}
	if depth > 0 {
		current.End = len(source)
		refs = append(refs, current)
	}
	return refs
}

func snippetNumber(value string) int64 {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != value {
		return 0
	}
	return n
}

// RewriteSnippets never gives a resolver author-supplied fallback HTML.
func RewriteSnippets(source string, resolve func(SnippetKey) string) string {
	refs := SnippetReferences(source)
	if len(refs) == 0 {
		return source
	}
	var out strings.Builder
	start := 0
	for _, ref := range refs {
		out.WriteString(source[start:ref.Start])
		if ref.ID > 0 && ref.Version > 0 {
			out.WriteString(resolve(ref.SnippetKey))
		}
		start = ref.End
	}
	out.WriteString(source[start:])
	return out.String()
}

// NormalizeSnippetShells removes every embedded fallback before any HTML
// policy can discard an invalid attribute and accidentally expose its children.
func NormalizeSnippetShells(source string) string {
	return RewriteSnippets(source, SnippetShell)
}

func UpdateSnippetReferences(source string, id, version int64) (string, int) {
	count := 0
	refs := SnippetReferences(source)
	var out strings.Builder
	start := 0
	for _, ref := range refs {
		if ref.ID != id || ref.Version <= 0 || ref.Version == version {
			continue
		}
		out.WriteString(source[start:ref.Start])
		out.WriteString(SnippetShell(SnippetKey{ID: id, Version: version}))
		start, count = ref.End, count+1
	}
	if count == 0 {
		return source, 0
	}
	out.WriteString(source[start:])
	return out.String(), count
}
