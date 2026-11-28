package content

import (
	"strings"
	"unicode/utf8"

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
	// Most stored labels and repeated query fields already satisfy the frozen
	// rules. Return that immutable string without allocating a second copy.
	// Once a change is needed, normalize only the remaining suffix; the prefix
	// was checked byte-for-byte and can be copied directly.
	for index, c := range value {
		changed := false
		if c < utf8.RuneSelf {
			changed = c >= 'A' && c <= 'Z' || c >= '\t' && c <= '\r' ||
				c == ' ' && (index == 0 || index+1 == len(value) || value[index-1] == ' ')
		} else if strings.ContainsRune(publicSearchSpaces, c) {
			changed = true
		} else if _, ok := publicSearchCase[c]; ok {
			changed = true
		} else if c == utf8.RuneError {
			_, size := utf8.DecodeRuneInString(value[index:])
			changed = size == 1
		}
		if !changed {
			continue
		}
		// Let the streaming normalizer handle a preceding space too. Otherwise
		// a following whitespace change could emit a duplicate/trailing space.
		if index > 0 && value[index-1] == ' ' {
			index--
		}
		var text publicSearchNormalizer
		text.out.Grow(len(value))
		text.out.WriteString(value[:index])
		for _, tail := range value[index:] {
			text.writeRune(tail)
		}
		return text.finish()
	}
	return value
}

// PublicSearchBody extracts static text nodes from already sanitized HTML.
// Attributes, comments, scripts and dynamic widget data are not searchable.
func PublicSearchBody(contentHTML string) string {
	tokens := html.NewTokenizer(strings.NewReader(contentHTML))
	var text publicSearchNormalizer
	ignored := 0
	for {
		kind := tokens.Next()
		switch kind {
		case html.ErrorToken:
			return text.finish()
		case html.TextToken:
			if ignored == 0 {
				text.write(tokens.Text())
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
				text.write([]byte{' '})
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
	return publicSearchScore(post, normalizedNeedle, PublicSearchBody(post.ContentHTML))
}

func publicSearchScore(post Post, normalizedNeedle, normalizedBody string) int {
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
	if strings.Contains(normalizedBody, normalizedNeedle) {
		score++
	}
	return score
}
