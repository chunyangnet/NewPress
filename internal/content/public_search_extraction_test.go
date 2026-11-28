package content

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Keep the pre-streaming contract independent of the optimized implementation.
// A comparison must also catch normalization across adjacent HTML text tokens,
// including byte sequences that become valid UTF-8 only after removing markup.
func referencePublicSearchNormalize(value string) string {
	var out strings.Builder
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

func referencePublicSearchBody(source string) string {
	tokens := html.NewTokenizer(strings.NewReader(source))
	var text strings.Builder
	ignored := 0
	for {
		kind := tokens.Next()
		switch kind {
		case html.ErrorToken:
			return referencePublicSearchNormalize(text.String())
		case html.TextToken:
			if ignored == 0 {
				text.Write(tokens.Text())
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := tokens.TagName()
			switch string(name) {
			case "script", "style", "template", "noscript", "head":
				if kind == html.StartTagToken {
					ignored++
				} else if kind == html.EndTagToken && ignored > 0 {
					ignored--
				}
			}
			if ignored == 0 {
				switch string(name) {
				case "address", "article", "aside", "blockquote", "br", "dd", "details", "dialog", "div", "dl", "dt",
					"fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header",
					"hr", "li", "main", "nav", "ol", "p", "pre", "section", "summary", "table", "tbody", "td", "th", "thead", "tr", "ul":
					text.WriteByte(' ')
				}
			}
		}
	}
}

func TestPublicSearchNormalizationAllCodePoints(t *testing.T) {
	var chunk strings.Builder
	for c := rune(0); c <= utf8.MaxRune; c++ {
		chunk.WriteRune(c)
		if c%1024 == 1023 || c == utf8.MaxRune {
			value := " \t" + chunk.String() + "\u3000\n"
			if got, want := NormalizePublicSearchText(value), referencePublicSearchNormalize(value); got != want {
				t.Fatalf("normalization differs in chunk ending U+%04X", c)
			}
			chunk.Reset()
		}
	}
}

func TestPublicSearchExtractionMatchesLegacyTokens(t *testing.T) {
	fixtures := []string{
		"", " plain ", "\x00\xff\xfe", "\xe4<b></b>\xb8\xad", "\xe4<!--ignored-->\xb8<em>\xad</em>",
		"\xe4<p>\xb8\xad</p>", "\xe4<script>ignored</script>\xb8\xad", "\xe4<b>\xb8</b>",
		"\xf0<i>\x9f</i>\x8c<b>\x90</b>", "\xc0<a>\xaf</a>", "\xe4<em>x</em>\xff",
		"<p>\u023a\u023e\u0130\u212a</p>", "<p>A &NotEqualTilde; B &amp; &#x3000; C</p>",
		"<p>A <em> </em> B</p><p>C</p>", "<pre>\n A\r\n B</pre>",
		"<template><p>ignored <template>nested</template></p></template>Visible",
		"<textarea>A &amp; <p>literal</textarea><script/>text</script>after",
		"<p>visible</p><img alt='hidden'/><!--hidden--><style>hidden</style>",
		"<svg><text>SVG text</text><title>SVG title</title></svg><p>last</p>",
	}
	random := rand.New(rand.NewPCG(3154, 20261127))
	parts := []string{"<p>", "</p>", "<b>", "</b>", "<script>", "</script>", "<template>", "</template>", "<br>", "<!--ignored-->", "&#x3000;", "&amp;", "&invalid;", " A ", "中", "É", "\xff", "\xe4", "\xb8", "\xad", "\r\n", "<", ">"}
	for range 500 {
		var value strings.Builder
		for range 40 {
			value.WriteString(parts[random.IntN(len(parts))])
		}
		fixtures = append(fixtures, value.String())
	}
	for i, source := range fixtures {
		if got, want := PublicSearchBody(source), referencePublicSearchBody(source); got != want {
			t.Fatalf("fixture %d (%q): got %q, want %q", i, source, got, want)
		}
	}
}

func FuzzPublicSearchExtraction(f *testing.F) {
	for _, source := range []string{"", "<p>中<em>文</em> &amp; É</p>", "\xe4<b>\xb8</b>\xad", "\xf0<em>\x9f\x8c</em>\x90", "a<!--ignored-->b<p>c</p>", "<template>hidden</template> visible", "<script/>hidden</script>"} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 16384 {
			t.Skip()
		}
		if got, want := PublicSearchBody(source), referencePublicSearchBody(source); got != want {
			t.Fatalf("source=%q got=%q want=%q", source, got, want)
		}
	})
}

func BenchmarkPublicSearchExtraction(b *testing.B) {
	for _, fixture := range []struct{ name, source string }{
		{"ascii-16k", "<p>" + strings.Repeat("ASCII BODY Search words ", 700) + "</p>"},
		{"chinese-16k", "<p>" + strings.Repeat("中文深度正文 É ", 700) + "</p>"},
		{"inline-16k", strings.Repeat("<p>中<em>文</em>完整 &amp; BODY</p>", 500)},
		{"attributes-256k", "<img alt='" + strings.Repeat("private ", 32768) + "'><p>visible</p>"},
		{"entities-64k", strings.Repeat("<span>&NotEqualTilde; &amp; &#x3000; É</span>", 1600)},
		{"plain-2m", "<p>" + strings.Repeat("content 中文 ", (2<<20)/15) + "</p>"},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			want := referencePublicSearchBody(fixture.source)
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture.source)))
			b.ResetTimer()
			for b.Loop() {
				if got := PublicSearchBody(fixture.source); got != want {
					b.Fatal("extraction changed")
				}
			}
		})
	}
}

func BenchmarkPublicSearchCacheOverflow(b *testing.B) {
	for _, fixture := range []struct{ rows, bytes int }{{12000, 1024}, {2000, 12288}} {
		b.Run(fmt.Sprintf("%d-%d", fixture.rows, fixture.bytes), func(b *testing.B) {
			r := NewMemoryRepository(false)
			stamp := time.Now().Add(-time.Hour)
			for i := range fixture.rows {
				body := fmt.Sprintf("<p>%d %s</p>", i, strings.Repeat("plain 中文 ", fixture.bytes/13))
				r.posts = append(r.posts, Post{ID: int64(i + 1), Type: TypePost, Status: StatusPublish,
					Title: "ordinary", Excerpt: "summary", ContentHTML: body, PublishedAt: stamp})
			}
			query := PublishedQuery{Search: "missing"}
			if _, err := r.SearchPublished(b.Context(), query); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				page, err := r.SearchPublished(b.Context(), query)
				if err != nil || page.Total != 0 {
					b.Fatal("overflow query", page.Total, err)
				}
			}
		})
	}
}
