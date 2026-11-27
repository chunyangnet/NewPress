package content

import (
	"strings"

	"golang.org/x/net/html"
)

var publicSearchCase = func() map[rune]rune {
	result := make(map[rune]rune)
	lower := []rune(publicSearchLower)
	for i, upper := range []rune(publicSearchUpper) {
		result[upper] = lower[i]
	}
	return result
}()

// NormalizePublicSearchText has a versioned, locale-independent SQL equivalent.
// It folds simple lowercase and whitespace, without stemming or accent removal.
func NormalizePublicSearchText(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	space := false
	for _, c := range value {
		if strings.ContainsRune(publicSearchSpaces, c) {
			space = out.Len() > 0
			continue
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		if lower, ok := publicSearchCase[c]; ok {
			c = lower
		}
		out.WriteRune(c)
	}
	return out.String()
}

// PublicSearchBody extracts static text nodes from already sanitized HTML.
// Attributes, comments, scripts and dynamic widget data are not searchable.
func PublicSearchBody(contentHTML string) string {
	tokens := html.NewTokenizer(strings.NewReader(contentHTML))
	var text strings.Builder
	text.Grow(len(contentHTML))
	ignored := 0
	for {
		kind := tokens.Next()
		switch kind {
		case html.ErrorToken:
			return NormalizePublicSearchText(text.String())
		case html.TextToken:
			if ignored == 0 {
				text.Write(tokens.Text())
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := tokens.TagName()
			tag := string(name)
			switch tag {
			case "script", "style", "template", "noscript", "head":
				if kind == html.StartTagToken {
					ignored++
				} else if kind == html.EndTagToken && ignored > 0 {
					ignored--
				}
			}
			if ignored == 0 && publicSearchBlock(tag) {
				text.WriteByte(' ')
			}
		}
	}
}

func publicSearchBlock(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "br", "dd", "details", "dialog", "div", "dl", "dt",
		"fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header",
		"hr", "li", "main", "nav", "ol", "p", "pre", "section", "summary", "table", "tbody", "td", "th", "thead", "tr", "ul":
		return true
	}
	return false
}

// PublicSearchScore counts each matching field group once, so repetition in a
// long body cannot overwhelm a title match. The SQL query uses the same weights.
func PublicSearchScore(post Post, normalizedNeedle string) int {
	if normalizedNeedle == "" {
		return 0
	}
	score := 0
	for _, field := range []struct {
		value  string
		weight int
	}{{post.Title, 64}, {post.Excerpt, 16}, {post.AuthorName, 4}} {
		if strings.Contains(NormalizePublicSearchText(field.value), normalizedNeedle) {
			score += field.weight
		}
	}
	for _, terms := range [][]Term{post.Categories, post.Tags} {
		for _, term := range terms {
			if strings.Contains(NormalizePublicSearchText(term.Name), normalizedNeedle) {
				score |= 8
				break
			}
		}
	}
	if strings.Contains(PublicSearchBody(post.ContentHTML), normalizedNeedle) {
		score++
	}
	return score
}
