package content

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPublicSearchFieldsNormalization(t *testing.T) {
	fixtures := []string{"", "ordinary", "中文 深度阅读", "already normalized 123", "a b c", "\uFFFD", "a \uFFFD b",
		" ", "  ", " leading", "trailing ", "a  b", "a   ", "a \tb", "a \n", "a \u00a0b", "a \u3000",
		"UPPER", "prefix Upper", "prefix É", "ÉCLAIR", "\u0130\u023a\u212a", "\x00", "a\x00b", "a \xff b", "\xff\xfe",
		"\xe4\xb8", "prefix \xf0\x9f", "\uFFFD\xff", "a\r\nb", "foo\u200bbar"}
	for c := rune(0); c <= utf8.MaxRune; c++ {
		if c >= 0xd800 && c <= 0xdfff {
			continue
		}
		for _, value := range []string{"prefix " + string(c) + " suffix", "prefix " + string(c)} {
			if got, want := NormalizePublicSearchText(value), referencePublicSearchNormalize(value); got != want {
				t.Fatalf("U+%04X input %q: got %q, want %q", c, value, got, want)
			}
		}
	}
	random := rand.New(rand.NewPCG(3154, 20261128))
	for range 2000 {
		value := make([]byte, random.IntN(512))
		for i := range value {
			value[i] = byte(random.UintN(256))
		}
		fixtures = append(fixtures, string(value))
	}
	for _, value := range fixtures {
		want := referencePublicSearchNormalize(value)
		if got := NormalizePublicSearchText(value); got != want || NormalizePublicSearchText(got) != got {
			t.Fatalf("input %q: got %q, want %q", value, got, want)
		}
	}
}

func FuzzPublicSearchFieldsNormalization(f *testing.F) {
	for _, value := range []string{"", "prefix  suffix", "prefix É", "prefix \u3000", "a \xff", "\uFFFD", "中文"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 16384 {
			t.Skip()
		}
		if got, want := NormalizePublicSearchText(value), referencePublicSearchNormalize(value); got != want {
			t.Fatalf("input %q: got %q, want %q", value, got, want)
		}
	})
}

func BenchmarkPublicSearchFieldsNormalization(b *testing.B) {
	for _, fixture := range []struct{ name, text string }{
		{"ascii-ready", "ordinary search title"}, {"chinese-ready", "中文 深度阅读"}, {"upper", "ARTICLE Title ÉCLAIR"},
		{"spaces", "  article \t title \u3000 "}, {"late-change", strings.Repeat("already normal ", 64) + "É"},
		{"long-ready", strings.Repeat("plain 中文 ", 512) + "last"},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			want := referencePublicSearchNormalize(fixture.text)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if NormalizePublicSearchText(fixture.text) != want {
					b.Fatal("normalization changed")
				}
			}
		})
	}
}
