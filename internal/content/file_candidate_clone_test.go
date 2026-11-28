package content

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/contenthtml"
)

func contentCandidateFixture(t *testing.T) *MemoryRepository {
	t.Helper()
	r := contentFileSnippetFixture(t, 3, 128, false)
	p := &r.posts[0]
	p.ContentSource += contenthtml.SnippetShell(contenthtml.SnippetKey{ID: 1, Version: 1})
	p.ContentHTML = contenthtml.Sanitize(p.ContentSource)
	p.Meta = map[string]any{"nested": map[string]any{"labels": []any{"original", map[string]any{"value": "original"}}}}
	p.Version = 3
	r.revisions[p.ID] = make([]Revision, 3, 8)
	for i := range r.revisions[p.ID] {
		snapshot := clonePost(*p)
		snapshot.Version = int64(i + 1)
		r.revisions[p.ID][i] = Revision{ID: int64(i + 1), PostID: p.ID, Version: snapshot.Version,
			Snapshot: snapshot, Actor: "editor", CreatedAt: p.UpdatedAt}
	}
	r.nextRevisionID = 4
	r.autosaves[p.ID] = map[string]Autosave{"user:7": {PostID: p.ID, OwnerKey: "user:7", SourceVersion: p.Version,
		Snapshot: clonePost(*p), UpdatedAt: p.UpdatedAt, ExpiresAt: p.UpdatedAt.Add(24 * time.Hour)}}
	r.series = []Series{{ID: 1, Version: 1, SeriesInput: SeriesInput{Kind: SeriesKindSequence, Status: StatusPublish, Title: "collection", Slug: "collection", PostIDs: []int64{p.ID}},
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}}
	r.nextSeriesID = 2
	// Published snapshots may have spare capacity left by previous mutations.
	// Appending to a candidate must still leave the old snapshot untouched.
	posts := make([]Post, len(r.posts), 8)
	copy(posts, r.posts)
	r.posts = posts
	snippets := make([]SnippetRecord, len(r.snippets), 8)
	copy(snippets, r.snippets)
	r.snippets = snippets
	r.indexSnippetReferencesLocked(*p)
	return r
}

func contentCandidateBytes(t *testing.T, state *MemoryRepository) []byte {
	t.Helper()
	raw, err := encodeContentDocument(state)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func changeCandidatePost(post *Post) {
	post.Title = "candidate only"
	post.Categories[0].Name = "candidate category"
	labels := post.Meta["nested"].(map[string]any)["labels"].([]any)
	labels[0] = "candidate label"
	labels[1].(map[string]any)["value"] = "candidate nested value"
}

func TestContentCandidateCloneIsolation(t *testing.T) {
	source := contentCandidateFixture(t)
	before := contentCandidateBytes(t, source)
	left, right := cloneContentState(source), cloneContentState(source)
	for _, candidate := range []*MemoryRepository{left, right} {
		if !bytes.Equal(before, contentCandidateBytes(t, candidate)) || !reflect.DeepEqual(source.autosaves, candidate.autosaves) {
			t.Fatal("clone lost state or changed persisted bytes")
		}
	}
	changeCandidatePost(&left.posts[0])
	changeCandidatePost(&left.revisions[1][0].Snapshot)
	autosave := left.autosaves[1]["user:7"]
	changeCandidatePost(&autosave.Snapshot)
	left.autosaves[1]["user:7"] = autosave
	left.autosaves[1]["user:8"] = autosave
	left.autosaves[2] = map[string]Autosave{}
	left.series[0].PostIDs[0] = 2
	left.snippets[0].Title = "candidate snippet"
	left.snippets[0].Revisions[0].Title = "candidate revision"
	left.posts = append(left.posts, Post{ID: 2})
	left.snippets = append(left.snippets, SnippetRecord{Snippet: Snippet{ID: 2}})
	left.series = append(left.series, Series{ID: 2})
	left.revisions[1] = append(left.revisions[1], Revision{ID: 4})
	left.revisions[2] = []Revision{{ID: 5}}
	left.snippetPostCounts[1] = []contenthtml.SnippetCount{{SnippetKey: contenthtml.SnippetKey{ID: 2, Version: 1}, Count: 2}}
	left.snippetPostCounts[2] = nil
	for _, retained := range []*MemoryRepository{source, right} {
		if !bytes.Equal(before, contentCandidateBytes(t, retained)) || len(retained.revisions) != 1 || len(retained.autosaves) != 1 || len(retained.autosaves[1]) != 1 ||
			len(retained.snippetPostCounts) != 1 || retained.snippetPostCounts[1][0].ID != 1 {
			t.Fatal("candidate changed a retained or sibling snapshot")
		}
	}
	changeCandidatePost(&source.posts[0])
	changeCandidatePost(&source.revisions[1][1].Snapshot)
	if !bytes.Equal(before, contentCandidateBytes(t, right)) {
		t.Fatal("reverse mutation changed a sibling snapshot")
	}
}

func TestContentCandidateCloneEmptyCollections(t *testing.T) {
	for _, allocated := range []bool{false, true} {
		source := NewMemoryRepository(false)
		if allocated {
			source.posts = []Post{}
			source.snippets = []SnippetRecord{}
			source.series = []Series{}
		}
		source.revisions[1] = nil
		source.revisions[2] = []Revision{}
		source.autosaves[1] = nil
		source.autosaves[2] = map[string]Autosave{}
		candidate := cloneContentState(source)
		if candidate.posts != nil || candidate.snippets != nil || candidate.series != nil || len(candidate.revisions) != 0 || len(candidate.autosaves) != 2 {
			t.Fatal("empty collection semantics changed")
		}
		candidate.autosaves[1]["new"] = Autosave{}
		candidate.revisions[3] = []Revision{{ID: 3}}
		if len(source.autosaves[1]) != 0 || len(source.revisions) != 2 {
			t.Fatal("empty map clone aliased its source")
		}
	}
}

func TestContentCandidateCloneCommitBoundary(t *testing.T) {
	for _, outcome := range []string{"commit", "cancel", "callback-error", "encode-error", "replace-error"} {
		t.Run(outcome, func(t *testing.T) {
			state := contentCandidateFixture(t)
			path := filepath.Join(t.TempDir(), "content.json")
			if _, err := writeContentState(t.Context(), path, state); err != nil {
				t.Fatal(err)
			}
			file, err := NewFileRepository(path, false)
			if err != nil {
				t.Fatal(err)
			}
			original, digest := file.state, file.digest
			before := contentCandidateBytes(t, original)
			if outcome == "replace-error" {
				if err := os.Mkdir(path+".bak", 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			prepared, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			defer finish()
			done := make(chan error, 1)
			go func() {
				_, err := fileContentChange(ctx, file, func(candidate *MemoryRepository) (Post, error) {
					post, err := candidate.ChangeStatus(ctx, 1, Mutation{ExpectedVersion: 3, Actor: "editor"}, StatusPrivate)
					if err == nil {
						// A canonical mutation clears autosaves and appends a deeply
						// detached revision before the candidate is published.
						candidate.posts[0].Meta["nested"].(map[string]any)["labels"].([]any)[0] = "committed"
						if outcome == "encode-error" {
							candidate.posts[0].Meta["invalid"] = make(chan int)
						}
					}
					close(prepared)
					<-release
					if outcome == "callback-error" {
						return Post{}, errors.New("injected candidate rejection")
					}
					return post, err
				})
				done <- err
			}()
			select {
			case <-prepared:
			case <-time.After(10 * time.Second):
				finish()
				<-done
				t.Fatal("candidate was not prepared")
			}
			for range 8 {
				post, err := original.GetByID(t.Context(), 1)
				if err != nil || post.Version != 3 || post.Status != StatusPublish {
					t.Error("old snapshot changed while preparing", post.Version, err)
				}
				post.Meta["nested"].(map[string]any)["labels"].([]any)[0] = "caller only"
			}
			if !bytes.Equal(before, contentCandidateBytes(t, original)) || file.searchState.Load() != original {
				t.Error("prepared candidate changed published state")
			}
			if outcome == "cancel" {
				cancel()
			}
			finish()
			err = <-done
			if (err == nil) != (outcome == "commit") || outcome == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("unexpected commit result", err)
			}
			if !bytes.Equal(before, contentCandidateBytes(t, original)) {
				t.Fatal("retained snapshot changed after commit or rejection")
			}
			if outcome == "commit" {
				if file.state == original || file.searchState.Load() != file.state || len(file.state.revisions[1]) != 4 || len(file.state.autosaves[1]) != 0 {
					t.Fatal("commit did not publish the complete candidate")
				}
			} else if file.state != original || file.digest != digest || file.searchState.Load() != original {
				t.Fatal("rejected candidate was published")
			}
			reopened, err := NewFileRepository(path, false)
			if err != nil || !bytes.Equal(contentCandidateBytes(t, reopened.state), contentCandidateBytes(t, file.state)) {
				t.Fatal("reopened state differs", err)
			}
		})
	}
}
