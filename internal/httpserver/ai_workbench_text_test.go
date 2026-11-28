package httpserver

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/aiworkbench"
	"newpress.local/newpress/internal/content"
)

func referenceWorkbenchText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}

func TestWorkbenchTextCompatibility(t *testing.T) {
	for _, value := range []string{"", "  \t\n", "ASCII text", "中文🧑🏽‍💻 e\u0301", "\xff\xfe\xed\xa0\x80x", strings.Repeat("x", 2<<20), strings.Repeat("中文🙂", 200000)} {
		for _, limit := range []int{0, 1, 2, 4, 7, 80, 200, 10000, math.MaxInt} {
			if got, want := workbenchText(value, limit), referenceWorkbenchText(value, limit); got != want {
				t.Fatalf("length=%d limit=%d changed output", len(value), limit)
			}
		}
	}
	rng := rand.New(rand.NewSource(20261128))
	for range 1000 {
		data := make([]byte, rng.Intn(2048))
		_, _ = rng.Read(data)
		limit := rng.Intn(2100)
		if workbenchText(string(data), limit) != referenceWorkbenchText(string(data), limit) {
			t.Fatal("random malformed UTF-8 changed")
		}
	}
}

func TestWorkbenchTextPreviewTruncation(t *testing.T) {
	for _, length := range []int{9999, 10000, 10001, 300000} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			s, _, _, repository, _, owner := newPersistentAdminTestServer(t)
			body := strings.Repeat("中", length)
			post := createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "workbench-text", "Title")
			post, err := repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Title: post.Title, Slug: post.Slug, Status: content.StatusPublish, Type: content.TypePost, ContentSource: body, PublishedAt: time.Now().Add(-time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := s.workbenchPreview(t.Context(), aiworkbench.Request{Task: "summary", Days: 7, PostID: post.ID})
			if err != nil {
				t.Fatal(err)
			}
			var facts struct {
				Body      string `json:"body"`
				Truncated bool   `json:"truncated"`
			}
			if err := json.Unmarshal([]byte(preview.Context), &facts); err != nil {
				t.Fatal(err)
			}
			if facts.Body != referenceWorkbenchText(body, 10000) || facts.Truncated != (length > 10000) {
				t.Fatal("preview text/truncation marker mismatch")
			}
		})
	}
}

var workbenchTextBenchmark string

func BenchmarkWorkbenchText(b *testing.B) {
	for _, size := range []int{1024, 128 << 10, 2 << 20} {
		for _, unicode := range []bool{false, true} {
			unit := "x"
			if unicode {
				unit = "中文🙂"
			}
			value := strings.Repeat(unit, size/len(unit))
			for _, method := range []string{"before", "bounded"} {
				b.Run(fmt.Sprintf("bytes=%d/unicode=%t/%s", size, unicode, method), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if method == "before" {
							workbenchTextBenchmark = referenceWorkbenchText(value, 10000)
						} else {
							workbenchTextBenchmark = workbenchText(value, 10000)
						}
					}
				})
			}
		}
	}
}
