package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

type snippetOutputScaleRepository struct {
	content.Repository
	lookups int
}

func (r *snippetOutputScaleRepository) ResolveSnippets(ctx context.Context, keys []contenthtml.SnippetKey) (map[contenthtml.SnippetKey]string, error) {
	r.lookups++
	return r.Repository.ResolveSnippets(ctx, keys)
}

// This opt-in comparison measures projection of a fixed page, excluding post
// selection, HTTP encoding, repository construction and fixture writes. The
// identical test can run against the pre-batch implementation too.
func TestSnippetOutputScale(t *testing.T) {
	if os.Getenv("NEWPRESS_SNIPPET_OUTPUT_SCALE") != "1" {
		t.Skip("set NEWPRESS_SNIPPET_OUTPUT_SCALE=1 for local projection measurements")
	}
	body := "<p>" + strings.Repeat("s", (32<<10)-7) + "</p>"
	access := content.SnippetAccess{Identity: "legacy:scale", Actor: "scale", Read: true, Manage: true}
	file, err := content.NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []struct {
		name string
		repo content.Repository
	}{{"memory", content.NewMemoryRepository(false)}, {"file", file}} {
		snippet, err := store.repo.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "scale", ContentSource: body, Visibility: content.SnippetShared}, access)
		if err != nil {
			t.Fatal(err)
		}
		shell := contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: snippet.CurrentVersion})
		original := pageView{Posts: make([]content.Post, 100)}
		var expected strings.Builder
		for i := range original.Posts {
			prefix := fmt.Sprintf("<h2>article-%03d</h2>", i)
			original.Posts[i] = content.Post{ID: int64(i + 1), ContentSource: prefix + shell, ContentHTML: prefix + shell}
			expected.WriteString(prefix + "<aside>" + body + "</aside>")
		}
		want := sha256.Sum256([]byte(expected.String()))
		repo := &snippetOutputScaleRepository{Repository: store.repo}
		s := &Server{repository: repo}
		for round := 1; round <= 3; round++ {
			view := original
			view.Posts = append([]content.Post(nil), original.Posts...)
			repo.lookups = 0
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			started := time.Now()
			found := s.prepareSnippets(t.Context(), &view)
			elapsed := time.Since(started)
			runtime.ReadMemStats(&after)
			hash := sha256.New()
			bytes := 0
			for _, post := range view.Posts {
				_, _ = hash.Write([]byte(post.ContentHTML))
				bytes += len(post.ContentHTML)
			}
			digest := fmt.Sprintf("%x", hash.Sum(nil))
			if !found || digest != fmt.Sprintf("%x", want) {
				t.Fatal("projection changed", found, digest)
			}
			entry, err := json.Marshal(map[string]any{"store": store.name, "round": round, "posts": len(view.Posts), "snippet_bytes": len(body), "lookups": repo.lookups, "milliseconds": float64(elapsed.Microseconds()) / 1000, "allocated_bytes": after.TotalAlloc - before.TotalAlloc, "response_body_bytes": bytes, "response_body_sha256": digest})
			if err != nil {
				t.Fatal(err)
			}
			t.Log(string(entry))
		}
	}
}
