package httpserver

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// Keep the former implementation as a compatibility oracle, including its
// different treatment of malformed UTF-8 in short and truncated strings.
func referenceAIContext(value string, limit int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "…[已截断]"
}

func TestAIContextCompatibility(t *testing.T) {
	cases := []struct{ name, value string }{
		{"empty", ""},
		{"whitespace", " \t\r\n\u00a0\u2003\u3000 "},
		{"ascii", " \nArticle text\t "},
		{"chinese", "\u3000中文评论与摘要\u00a0"},
		{"emoji", " \t🧑🏽‍💻hello e\u0301🇨🇳\n"},
		{"invalid-single", "\xff"},
		{"invalid-run", " \xff\xfe\xfd abc \xf0\x9f\x92 "},
		{"invalid-boundary", "中\xc0\xaf\xed\xa0\x80\x80🙂tail"},
		{"embedded-space", "  abc\x00\t\n中文\u2003tail  "},
		{"long-ascii", strings.Repeat("a", 2<<20)},
		{"long-unicode", strings.Repeat("中文🙂", (2<<20)/10)},
		{"long-trailing-space", "你好 " + strings.Repeat("\u3000", 10000)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			for _, limit := range []int{0, 1, 2, 3, 4, 7, 60, 300, 1000, 5000, 16000, math.MaxInt} {
				got, want := aiCommentContext(test.value, limit), referenceAIContext(test.value, limit)
				if got != want {
					t.Fatalf("limit %d: got %q; want %q", limit, got[:min(len(got), 100)], want[:min(len(want), 100)])
				}
			}
		})
	}
}

func TestAIContextRandomBytes(t *testing.T) {
	random := rand.New(rand.NewSource(20261128))
	for i := range 1000 {
		data := make([]byte, random.Intn(1024))
		if _, err := random.Read(data); err != nil {
			t.Fatal(err)
		}
		limit := random.Intn(1200)
		if got, want := aiCommentContext(string(data), limit), referenceAIContext(string(data), limit); got != want {
			t.Fatalf("iteration %d, limit %d: got %q; want %q", i, limit, got, want)
		}
	}
}

func TestAIContextNegativeLimit(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("negative limit must remain a programming error")
		}
	}()
	aiCommentContext("body", -1)
}

func FuzzAIContextCompatibility(f *testing.F) {
	for _, seed := range []string{"", "\u3000中文🙂 body \n", "\xff\xfe\xf0\x9f\x92", strings.Repeat("a", 6000)} {
		f.Add(seed, uint16(3))
		f.Add(seed, uint16(5000))
	}
	f.Fuzz(func(t *testing.T, value string, limit uint16) {
		if len(value) > 2<<20 {
			t.Skip()
		}
		if got, want := aiCommentContext(value, int(limit)), referenceAIContext(value, int(limit)); got != want {
			t.Fatalf("context changed at limit %d", limit)
		}
	})
}

var benchmarkAIContextResult string

func BenchmarkAIContext(b *testing.B) {
	for _, encoding := range []string{"ascii", "unicode"} {
		unit := "a"
		if encoding == "unicode" {
			unit = "中文🙂"
		}
		for _, size := range []int{1024, 128 << 10, 2 << 20} {
			for _, limit := range []int{5000, 16000} {
				b.Run(fmt.Sprintf("%s/bytes=%d/limit=%d", encoding, size, limit), func(b *testing.B) {
					value := strings.Repeat(unit, size/len(unit))
					b.ReportAllocs()
					for b.Loop() {
						benchmarkAIContextResult = aiCommentContext(value, limit)
					}
				})
			}
		}
	}
}
