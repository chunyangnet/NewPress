package content

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Pause a search once it is inside its captured state, without changing the
// production repository or holding a cache lock needed by a writer.
type pausedSearchContext struct {
	context.Context
	calls   atomic.Int32
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func newPausedSearchContext(ctx context.Context) *pausedSearchContext {
	return &pausedSearchContext{Context: ctx, entered: make(chan struct{}), resume: make(chan struct{})}
}

func (ctx *pausedSearchContext) Err() error {
	if ctx.calls.Add(1) == 3 {
		close(ctx.entered)
		select {
		case <-ctx.resume:
		case <-ctx.Context.Done():
		}
	}
	return ctx.Context.Err()
}

func (ctx *pausedSearchContext) release() { ctx.once.Do(func() { close(ctx.resume) }) }

type snapshotSearchResult struct {
	page PublishedPage
	err  error
}

func snapshotSearchFixture(t *testing.T) (*FileRepository, []Post) {
	t.Helper()
	r, err := NewFileRepository(filepath.Join(t.TempDir(), "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	var posts []Post
	for n := range 3 {
		p, err := r.Create(t.Context(), PostInput{Title: fmt.Sprintf("original %d", n), Slug: fmt.Sprintf("snapshot-%d", n),
			Status: StatusPublish, Excerpt: "summary", ContentSource: "<p>original needle 中文</p>",
			Category: "旧分类", TagNames: []string{"旧标签"}, PublishedAt: time.Date(2026, 1, 1, n, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		posts = append(posts, p)
	}
	return r, posts
}

func TestPublicSearchFileSnapshotDoesNotBlockCommit(t *testing.T) {
	for _, change := range []string{"edit", "withdraw", "delete", "terms", "failed-commit"} {
		t.Run(change, func(t *testing.T) {
			r, posts := snapshotSearchFixture(t)
			if change == "delete" {
				var err error
				posts[0], err = r.ChangeStatus(t.Context(), posts[0].ID, Mutation{ExpectedVersion: posts[0].Version}, StatusTrash)
				if err != nil {
					t.Fatal(err)
				}
			}
			query := PublishedQuery{Search: "needle", Sort: "title", Limit: 2}
			before, err := r.SearchPublished(t.Context(), query)
			if err != nil {
				t.Fatal(err)
			}
			oldState := r.state
			oldPosts := make([]Post, len(oldState.posts))
			for i, p := range oldState.posts {
				oldPosts[i] = clonePost(p)
			}
			if change == "failed-commit" {
				if err := os.Mkdir(r.path+".bak", 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx := newPausedSearchContext(t.Context())
			defer ctx.release()
			result := make(chan snapshotSearchResult, 1)
			go func() {
				page, err := r.SearchPublished(ctx, query)
				result <- snapshotSearchResult{page, err}
			}()
			select {
			case <-ctx.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("search did not enter its snapshot")
			}
			write := make(chan error, 1)
			go func() {
				var err error
				switch change {
				case "edit", "failed-commit":
					_, err = r.Update(t.Context(), posts[0].ID, Mutation{ExpectedVersion: posts[0].Version}, PostInput{
						Title: "new title", Slug: posts[0].Slug, Status: StatusPublish, Excerpt: "summary", ContentSource: "<p>changed body</p>",
						CategoryIDs: []int64{posts[0].Categories[0].ID}, TagNames: []string{"changed tag"}, PublishedAt: posts[0].PublishedAt})
				case "withdraw":
					var items []BulkMutation
					for _, p := range posts {
						items = append(items, BulkMutation{ID: p.ID, ExpectedVersion: p.Version})
					}
					_, err = r.BulkChangeStatus(t.Context(), BulkStatusChange{Items: items, Status: StatusPrivate})
				case "delete":
					_, err = r.BulkChangeStatus(t.Context(), BulkStatusChange{Delete: true, Status: StatusTrash,
						Items: []BulkMutation{{ID: posts[0].ID, ExpectedVersion: posts[0].Version}}})
				case "terms":
					term := posts[0].Categories[0]
					_, err = r.UpdateTerm(t.Context(), term.ID, TermMutation{ExpectedVersion: term.Version}, TermInput{Taxonomy: term.Taxonomy, Slug: term.Slug, Name: "新分类"})
					if err == nil {
						term = posts[0].Tags[0]
						err = r.DeleteTerm(t.Context(), term.ID, TermMutation{ExpectedVersion: term.Version})
					}
				}
				write <- err
			}()
			select {
			case err := <-write:
				if (err != nil) != (change == "failed-commit") {
					t.Fatal("unexpected write result", err)
				}
			case <-time.After(5 * time.Second):
				ctx.release()
				<-result
				<-write
				t.Fatal("paused search blocked the file commit")
			}
			if !reflect.DeepEqual(oldState.posts, oldPosts) {
				t.Fatal("write changed the posts held by the search snapshot")
			}
			if (r.state == oldState) != (change == "failed-commit") {
				t.Fatal("only a successful durable commit may replace the state")
			}
			after, err := r.SearchPublished(t.Context(), query)
			if err != nil {
				t.Fatal(err)
			}
			want := before.Total
			if change == "edit" {
				want--
			}
			if change == "withdraw" {
				want = 0
			}
			if after.Total != want {
				t.Fatal("new query did not observe committed visibility", after.Total, want)
			}
			if change == "terms" {
				for _, p := range after.Items {
					if len(p.Tags) != 0 || len(p.Categories) != 1 || p.Categories[0].Name != "新分类" {
						t.Fatal("new query did not observe committed terms")
					}
				}
			}
			ctx.release()
			got := <-result
			if got.err != nil || !reflect.DeepEqual(got.page, before) {
				t.Fatal("old query mixed states", got.err)
			}
			reopened, err := NewFileRepository(r.path, false)
			if err != nil {
				t.Fatal(err)
			}
			persisted, err := reopened.SearchPublished(t.Context(), query)
			left, _ := json.Marshal(after)
			right, _ := json.Marshal(persisted)
			if err != nil || !bytes.Equal(left, right) {
				t.Fatal("reopened search differs from the committed page", err)
			}
		})
	}
}

func TestPublicSearchFileSnapshotCancellation(t *testing.T) {
	r, _ := snapshotSearchFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// A previously cancelled call should not wait for an unrelated file writer.
	r.mu.Lock()
	result := make(chan error, 1)
	go func() {
		_, err := r.SearchPublished(ctx, PublishedQuery{Search: "needle"})
		result <- err
	}()
	select {
	case err := <-result:
		r.mu.Unlock()
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		r.mu.Unlock()
		<-result
		t.Fatal("cancelled search waited for the file writer")
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	paused := newPausedSearchContext(ctx)
	defer paused.release()
	go func() {
		_, err := r.SearchPublished(paused, PublishedQuery{Search: "needle"})
		result <- err
	}()
	<-paused.entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal("captured search ignored cancellation", err)
	}
}

func TestPublicSearchFileSnapshotWhileWriterPrepares(t *testing.T) {
	for _, outcome := range []string{"commit", "cancel", "failed-replace"} {
		t.Run(outcome, func(t *testing.T) {
			r, posts := snapshotSearchFixture(t)
			query := PublishedQuery{Search: "needle"}
			before, err := r.SearchPublished(t.Context(), query)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "failed-replace" {
				if err := os.Mkdir(r.path+".bak", 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			prepared, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			defer finish()
			write := make(chan error, 1)
			go func() {
				_, err := fileContentChange(ctx, r, func(candidate *MemoryRepository) (Post, error) {
					p, err := candidate.ChangeStatus(ctx, posts[0].ID, Mutation{ExpectedVersion: posts[0].Version}, StatusPrivate)
					close(prepared)
					<-release
					return p, err
				})
				write <- err
			}()
			<-prepared
			result := make(chan snapshotSearchResult, 1)
			go func() {
				page, err := r.SearchPublished(t.Context(), query)
				result <- snapshotSearchResult{page, err}
			}()
			select {
			case got := <-result:
				if got.err != nil || !reflect.DeepEqual(got.page, before) {
					t.Fatal("query observed an uncommitted candidate", got.err)
				}
			case <-time.After(2 * time.Second):
				finish()
				<-write
				<-result
				t.Fatal("query waited for a prepared file writer")
			}
			if outcome == "cancel" {
				cancel()
			}
			finish()
			if err := <-write; (err == nil) != (outcome == "commit") {
				t.Fatal("unexpected write outcome", err)
			}
			after, err := r.SearchPublished(t.Context(), query)
			want := before.Total
			if outcome == "commit" {
				want--
			}
			if err != nil || after.Total != want {
				t.Fatal("published search state did not follow durable outcome", after.Total, want, err)
			}
		})
	}
}
