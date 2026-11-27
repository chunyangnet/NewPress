package contenthtml

import (
	"strings"
	"testing"
)

func TestSnippetShellSanitization(t *testing.T) {
	for name, source := range map[string]string{
		"valid":      `<aside data-np-snippet="1" data-np-snippet-version="2">SECRET</aside>`,
		"nested":     `<aside data-np-snippet="1" data-np-snippet-version="2"><aside>SECRET</aside>SECRET</aside>`,
		"invalid":    `<aside data-np-snippet="bad" data-np-snippet-version="2">SECRET</aside>`,
		"missing":    `<aside data-np-snippet-version="2">SECRET</aside>`,
		"duplicate":  `<aside data-np-snippet="1" data-np-snippet="2" data-np-snippet-version="2">SECRET</aside>`,
		"overflow":   `<aside data-np-snippet="9999999999999999999" data-np-snippet-version="2">SECRET</aside>`,
		"unclosed":   `<aside data-np-snippet="1" data-np-snippet-version="2">SECRET`,
		"selfclosed": `<aside data-np-snippet="1" data-np-snippet-version="2"/>SECRET`,
		"uppercase":  `<ASIDE DATA-NP-SNIPPET="1" DATA-NP-SNIPPET-VERSION="2">SECRET</ASIDE>`,
	} {
		t.Run(name, func(t *testing.T) {
			got := Sanitize(source)
			if strings.Contains(got, "SECRET") {
				t.Fatalf("fallback leaked: %s", got)
			}
			if again := Sanitize(got); again != got {
				t.Fatalf("not stable: %q / %q", got, again)
			}
			expanded := RewriteSnippets(got, func(k SnippetKey) string { return "<aside><p>resolved</p></aside>" })
			if strings.Contains(expanded, "data-np-snippet") || strings.Contains(expanded, "SECRET") {
				t.Fatal(expanded)
			}
		})
	}
}

func TestSnippetRewritePreservesUnrelatedSourceBytes(t *testing.T) {
	left := `<p title='unchanged' >A &amp; B</p>` + "\n\n"
	right := "\t<!--retain-->" + SnippetShell(SnippetKey{9, 1})
	source := left + `<aside data-np-snippet='3' data-np-snippet-version='1'>discard</aside>` + right
	got, n := UpdateSnippetReferences(source, 3, 12)
	if n != 1 || got != left+SnippetShell(SnippetKey{3, 12})+right {
		t.Fatal(n, got)
	}
	if got, n = UpdateSnippetReferences(source, 4, 1); n != 0 || got != source {
		t.Fatal("unrelated mutation", got)
	}
	if refs := SnippetReferences(`<pre>&lt;aside data-np-snippet="3"&gt;</pre><!--<aside data-np-snippet="3">-->`); len(refs) != 0 {
		t.Fatal(refs)
	}
}
