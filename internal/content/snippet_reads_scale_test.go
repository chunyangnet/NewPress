package content

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/contenthtml"
)

// Opt-in measurements use a valid, near-budget fixture. They are observations,
// not timing assertions, and do not measure file writes or process residency.
func TestSnippetReadScale(t *testing.T) {
	if os.Getenv("NEWPRESS_SNIPPET_READ_SCALE") != "1" {
		t.Skip("set NEWPRESS_SNIPPET_READ_SCALE=1 for local scale measurements")
	}
	r := NewMemoryRepository(false)
	a := snippetManager()
	s, err := NextSnippetRecord(SnippetRecord{}, 1, Mutation{}, SnippetInput{Title: "scale", ContentSource: "<p>" + strings.Repeat("x", 1450) + "</p>", Visibility: SnippetShared}, a, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	first := s.Revisions[0]
	for i := int64(2); i <= MaxSnippetRevisions; i++ {
		rev := first
		rev.Version = i
		s.Revisions = append(s.Revisions, rev)
	}
	s.Version, s.CurrentVersion = MaxSnippetRevisions, MaxSnippetRevisions
	if err = ValidateSnippetState([]SnippetRecord{s}, nil, 2); err != nil {
		t.Fatal(err)
	}
	r.snippets, r.nextSnippetID, r.contentSchema = []SnippetRecord{s}, 2, 2
	plain := "<p>" + strings.Repeat("p", 1000) + "</p>"
	referenced := plain + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 1, Version: 1}) + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 1, Version: MaxSnippetRevisions})
	p, err := r.Create(t.Context(), PostInput{Title: "post", Slug: "scale-1", ContentSource: referenced})
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(2); i <= 10000; i++ {
		next := p
		next.ID, next.Title, next.Slug = i, fmt.Sprint("post ", i), fmt.Sprint("scale-", i)
		next.ContentSource = plain
		if (i-1)%10 == 0 {
			next.ContentSource = referenced
		}
		next.ContentHTML = contenthtml.Sanitize(next.ContentSource)
		r.posts = append(r.posts, next)
		r.indexSnippetReferencesLocked(next)
	}
	r.nextID = 10001
	payload, err := encodeContentDocument(r)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "content.json")
	if err = os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	file, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture revisions=%d snippet_logical_bytes=%d posts=%d file_bytes=%d file_open_ms=%.3f", len(s.Revisions), SnippetStorageSize(s), len(r.posts), len(payload), float64(time.Since(started).Microseconds())/1000)
	canonical := func() SnippetImpacts {
		out := SnippetImpacts{Snippet: s.Snippet, Items: []SnippetImpact{}}
		for _, post := range r.posts {
			v := SnippetPostImpact(post, 1, s.CurrentVersion)
			if v.References > 0 {
				out.Items = append(out.Items, v)
			}
		}
		out.Total = len(out.Items)
		out.Items = out.Items[:min(24, len(out.Items))]
		return out
	}
	want := canonical()
	for _, store := range []struct {
		name       string
		repository SnippetRepository
	}{{"memory", r}, {"file", file}} {
		got, err := store.repository.SnippetImpacts(t.Context(), 1, a, 24, 0)
		if err != nil || !reflect.DeepEqual(got, want) || got.Total != 1000 {
			t.Fatal(got, err)
		}
		for _, operation := range []string{"summary_json", "full_history_json", "indexed_impact", "canonical_scan"} {
			for round := 1; round <= 3; round++ {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				started := time.Now()
				bytes := 0
				switch operation {
				case "summary_json":
					page, e := store.repository.GetSnippetPage(t.Context(), 1, a, 0, 24)
					if e != nil || len(page.History) != 24 {
						t.Fatal(e)
					}
					raw, e := json.Marshal(page)
					if e != nil {
						t.Fatal(e)
					}
					bytes = len(raw)
				case "full_history_json":
					record, e := store.repository.GetSnippet(t.Context(), 1, a)
					if e != nil || len(record.Revisions) != MaxSnippetRevisions {
						t.Fatal(e)
					}
					raw, e := json.Marshal(record)
					if e != nil {
						t.Fatal(e)
					}
					bytes = len(raw)
				case "indexed_impact":
					got, e := store.repository.SnippetImpacts(t.Context(), 1, a, 24, 0)
					if e != nil || !reflect.DeepEqual(got, want) {
						t.Fatal(e)
					}
				case "canonical_scan":
					if !reflect.DeepEqual(canonical(), want) {
						t.Fatal("scan differs")
					}
				}
				elapsed := time.Since(started)
				runtime.ReadMemStats(&after)
				entry, _ := json.Marshal(map[string]any{"store": store.name, "operation": operation, "round": round, "milliseconds": float64(elapsed.Microseconds()) / 1000, "allocated_bytes": after.TotalAlloc - before.TotalAlloc, "response_bytes": bytes})
				t.Log(string(entry))
			}
		}
	}
}
