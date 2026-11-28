package comment

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func commentReadFixture(size int) *FileStore {
	s, _ := NewFileStore("", false)
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= size; i++ {
		body := fmt.Sprintf("Message %d 留言 Needle", i%11)
		c := Comment{ID: int64(i), PostID: int64(i%7 + 1), UserID: int64(i % 5), Version: 1,
			AuthorName: fmt.Sprintf("Author %d", i%3), AuthorEmail: fmt.Sprintf("name%d@example.test", i%5),
			ContentSource: body, ContentHTML: RenderPlainText(body), Status: []string{StatusPending, StatusApproved, StatusSpam, StatusTrash}[i%4],
			CreatedAt: base.Add(time.Duration(i*13%31) * time.Second), UpdatedAt: base.Add(time.Duration(i*17%37) * time.Second)}
		s.items[c.ID] = c
	}
	s.nextID, s.updatedAt = int64(size+1), base.Add(time.Hour)
	return s
}

func TestCommentReadQueriesMatchReference(t *testing.T) {
	s := commentReadFixture(457)
	random := rand.New(rand.NewPCG(4281, 9027))
	for i := range 800 {
		query := Query{
			PostID: int64(random.IntN(9)), UserID: []int64{0, 1, 3, 999}[random.IntN(4)], OwnOnly: random.IntN(2) == 0,
			Status: []string{"", " pending ", StatusApproved, StatusSpam, StatusTrash, "bad"}[random.IntN(6)],
			Search: []string{"", "NEEDLE", "留言", "AUTHOR", "@example", "0 name", "absent", "\xc2"}[random.IntN(8)],
			Limit:  []int{-1, 0, 1, 24, 200, 201}[random.IntN(6)],
			Offset: []int{0, 1, 20, 48, 100, 1001, int(^uint(0) >> 1)}[random.IntN(7)],
		}
		want, wantErr := referenceCommentAdmin(s, query)
		got, err := s.ListAdmin(t.Context(), query)
		if !errors.Is(err, wantErr) || !reflect.DeepEqual(got, want) {
			t.Fatalf("query %d %+v differs: got=%+v err=%v want=%+v err=%v", i, query, got, err, want, wantErr)
		}
	}
	// Explicitly exercise ordering ties, boundary-spanning search and owner-only
	// status counts independently of the randomized filter combinations.
	for _, query := range []Query{{Limit: 24}, {Limit: 24, Offset: 24}, {Search: "0 name"}, {Search: "@example"}, {UserID: 1, OwnOnly: true}, {UserID: 1, OwnOnly: true, Search: "@example"}, {PostID: 1, Status: StatusApproved}} {
		want, _ := referenceCommentAdmin(s, query)
		got, err := s.ListAdmin(t.Context(), query)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("explicit query %+v: got=%+v want=%+v err=%v", query, got, want, err)
		}
	}
	for postID := int64(1); postID <= 8; postID++ {
		for _, limit := range []int{0, 1, 24, 201} {
			for _, offset := range []int{0, 1, 20, 500, int(^uint(0) >> 1)} {
				want, wantTotal, _ := referenceCommentApproved(s, postID, limit, offset)
				got, total, err := s.ListApproved(t.Context(), postID, limit, offset)
				if err != nil || total != wantTotal || !reflect.DeepEqual(got, want) {
					t.Fatalf("public post=%d limit=%d offset=%d: total=%d want=%d err=%v", postID, limit, offset, total, wantTotal, err)
				}
			}
		}
	}
}

func TestCommentReadIsolationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "comments.json")
	s := commentReadFixture(57)
	s.path = path
	if err := s.persistLocked(s.items, s.revisions, s.nextID, s.updatedAt, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	check := func(store *FileStore) {
		t.Helper()
		want, _ := referenceCommentAdmin(store, Query{Limit: 20})
		got, err := store.ListAdmin(t.Context(), Query{Limit: 20})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("persisted page differs", err)
		}
		got.Items[0].AuthorEmail, got.Items[0].ContentSource = "changed@example.test", "changed"
		got.StatusCounts[StatusPending] = 9999
		again, err := store.ListAdmin(t.Context(), Query{Limit: 20})
		if err != nil || !reflect.DeepEqual(again, want) {
			t.Fatal("page aliases store", err)
		}
		snapshot, err := store.Snapshot(t.Context())
		if err != nil || snapshot.Total != len(store.items) || !reflect.DeepEqual(snapshot.StatusCounts, want.StatusCounts) || !snapshot.UpdatedAt.Equal(store.updatedAt) {
			t.Fatal("snapshot differs", snapshot, err)
		}
		snapshot.StatusCounts[StatusPending] = 9999
		if again, _ := store.Snapshot(t.Context()); !reflect.DeepEqual(again.StatusCounts, want.StatusCounts) {
			t.Fatal("snapshot count map aliases store")
		}
	}
	check(s)
	check(loaded)
	c, err := loaded.Get(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	c, err = loaded.ChangeStatus(t.Context(), c.ID, Mutation{ExpectedVersion: c.Version}, StatusTrash)
	if err != nil {
		t.Fatal(err)
	}
	check(loaded)
	if err = loaded.Delete(t.Context(), c.ID, Mutation{ExpectedVersion: c.Version}); err != nil {
		t.Fatal(err)
	}
	check(loaded)
}

type commentReadCancelContext struct {
	context.Context
	cancel    context.CancelFunc
	at, calls int
}

func (c *commentReadCancelContext) Err() error {
	c.calls++
	if c.calls == c.at {
		c.cancel()
	}
	return c.Context.Err()
}

func commentReadOperation(ctx context.Context, s *FileStore, operation string) (bool, error) {
	switch operation {
	case "admin":
		v, err := s.ListAdmin(ctx, Query{Limit: 24})
		return reflect.DeepEqual(v, List{}), err
	case "public":
		v, total, err := s.ListApproved(ctx, 1, 24, 0)
		return v == nil && total == 0, err
	case "snapshot":
		v, err := s.Snapshot(ctx)
		return reflect.DeepEqual(v, Snapshot{}), err
	case "get":
		v, err := s.Get(ctx, 1)
		return v == (Comment{}), err
	case "revisions":
		v, err := s.ListRevisions(ctx, 1, 20)
		return v == nil, err
	default:
		return true, s.Ready(ctx)
	}
}

func TestCommentReadCancellation(t *testing.T) {
	for _, operation := range []string{"admin", "public", "snapshot", "get", "revisions", "ready"} {
		for _, boundary := range []string{"entry", "lock", "output"} {
			t.Run(operation+"/"+boundary, func(t *testing.T) {
				s := commentReadFixture(1024)
				s.revisions[1] = []Revision{{CommentID: 1, Version: 1, ContentSource: "PRIVATE-REVISION"}}
				parent, cancel := context.WithCancel(t.Context())
				defer cancel()
				var ctx context.Context = parent
				switch boundary {
				case "entry":
					cancel()
				case "lock":
					observed := &stateObservedContext{Context: parent, entered: make(chan struct{})}
					ctx = observed
					s.mu.Lock()
					go func() {
						select {
						case <-observed.entered:
						case <-time.After(2 * time.Second):
						}
						cancel()
						s.mu.Unlock()
					}()
				case "output":
					at := 3
					if operation == "admin" || operation == "public" || operation == "snapshot" {
						at = 4 // Cancellation after some of the scan has run.
					}
					ctx = &commentReadCancelContext{Context: parent, cancel: cancel, at: at}
				}
				empty, err := commentReadOperation(ctx, s, operation)
				if !errors.Is(err, context.Canceled) || !empty {
					t.Fatal("cancelled read returned values", empty, err)
				}
			})
		}
	}
}

func TestCommentReadConcurrentMutation(t *testing.T) {
	s := commentReadFixture(200)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 80 {
				list, err := s.ListAdmin(t.Context(), Query{Limit: 24})
				if err != nil || list.Total != 200 || len(list.Items) != 24 {
					t.Error("inconsistent page", list.Total, len(list.Items), err)
					return
				}
				counts := 0
				for _, value := range list.StatusCounts {
					counts += value
				}
				ordered := append([]Comment(nil), list.Items...)
				SortAdmin(ordered)
				if counts != 200 || !reflect.DeepEqual(ordered, list.Items) {
					t.Error("inconsistent ordering/counts")
				}
				list.Items[0].ContentSource = "outside"
				if _, _, err = s.ListApproved(t.Context(), 1, 24, 0); err != nil {
					t.Error(err)
				}
				if v, err := s.Snapshot(t.Context()); err != nil || v.Total != 200 {
					t.Error("inconsistent snapshot", v, err)
				}
			}
		})
	}
	for range 40 {
		c, err := s.Get(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(c.ContentSource, "outside") {
			t.Fatal("read mutated canonical comment")
		}
		if _, err = s.ChangeStatus(t.Context(), c.ID, Mutation{ExpectedVersion: c.Version}, StatusApproved); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestCommentReadFinalCancellation(t *testing.T) {
	for _, operation := range []string{"admin", "public", "snapshot"} {
		for _, size := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s/size-%d", operation, size), func(t *testing.T) {
				s := commentReadFixture(size)
				parent, cancel := context.WithCancel(t.Context())
				defer cancel()
				at := 5 // Entry, lock, scan, before page assembly, before returning it.
				if operation == "snapshot" {
					at--
				}
				if size == 0 {
					at--
				}
				ctx := &commentReadCancelContext{Context: parent, cancel: cancel, at: at}
				empty, err := commentReadOperation(ctx, s, operation)
				if !errors.Is(err, context.Canceled) || !empty {
					t.Fatal("last cancellation boundary returned a page", empty, err)
				}
			})
		}
	}
}

func BenchmarkCommentRead(b *testing.B) {
	for _, size := range []int{100, 10000} {
		s := commentReadFixture(size)
		for _, kind := range []string{"admin", "admin-deep", "admin-empty", "own", "public", "snapshot"} {
			b.Run(fmt.Sprintf("items-%d/%s", size, kind), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					var err error
					switch kind {
					case "admin":
						_, err = s.ListAdmin(b.Context(), Query{Limit: 24})
					case "admin-deep":
						_, err = s.ListAdmin(b.Context(), Query{Limit: 24, Offset: size / 2})
					case "admin-empty":
						_, err = s.ListAdmin(b.Context(), Query{Limit: 24, Offset: size + 1})
					case "own":
						_, err = s.ListAdmin(b.Context(), Query{Limit: 24, OwnOnly: true, UserID: 1})
					case "public":
						_, _, err = s.ListApproved(b.Context(), 1, 24, 0)
					case "snapshot":
						_, err = s.Snapshot(b.Context())
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
