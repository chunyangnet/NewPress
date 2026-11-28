package comment

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

type approvedFallback struct {
	Store
	calls []int64
	err   error
	after func()
}

func (s *approvedFallback) ListApproved(ctx context.Context, id int64, limit, offset int) ([]Comment, int, error) {
	s.calls = append(s.calls, id)
	if s.after != nil {
		s.after()
	}
	if s.err != nil {
		return nil, 0, s.err
	}
	return s.Store.ListApproved(ctx, id, limit, offset)
}

type approvedBatch struct {
	Store
	result map[int64]int
	err    error
	after  func()
	calls  int
}

func (s *approvedBatch) CountApprovedByPost(context.Context, []int64) (map[int64]int, error) {
	s.calls++
	if s.after != nil {
		s.after()
	}
	return s.result, s.err
}

func TestApprovedCountsMatchListAndFallback(t *testing.T) {
	s := commentReadFixture(8192)
	for _, ids := range [][]int64{nil, {1}, {1, 2, 3, 4, 5, 6, 7, 99}, {7, 1, 1, 7, 99}} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprint(ids, fallback), func(t *testing.T) {
				var store Store = s
				legacy := &approvedFallback{Store: s}
				if fallback {
					store = legacy
				}
				got, err := ReadApprovedCounts(t.Context(), store, ids)
				want := map[int64]int{}
				for _, id := range ids {
					_, n, e := s.ListApproved(t.Context(), id, 1, 0)
					if e != nil {
						t.Fatal(e)
					}
					want[id] = n
				}
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatal("counts differ from approved list", err, got, want)
				}
				if fallback && len(legacy.calls) != len(want) {
					t.Fatal("duplicate fallback reads", legacy.calls)
				}
				got[1] = 999999
				again, _ := ReadApprovedCounts(t.Context(), store, []int64{1})
				if again[1] == 999999 {
					t.Fatal("caller mutated stored counts")
				}
			})
		}
	}
}

func TestApprovedCountsErrorAndCancellation(t *testing.T) {
	s := commentReadFixture(2048)
	for _, boundary := range []string{"entry", "lock", "scan", "fallback", "batch"} {
		t.Run(boundary, func(t *testing.T) {
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			var ctx context.Context = parent
			var store Store = s
			switch boundary {
			case "entry":
				cancel()
			case "lock":
				observed := &stateObservedContext{Context: parent, entered: make(chan struct{})}
				ctx = observed
				s.mu.Lock()
				go func() { <-observed.entered; cancel(); s.mu.Unlock() }()
			case "scan":
				ctx = &commentReadCancelContext{Context: parent, cancel: cancel, at: 5}
			case "fallback":
				store = &approvedFallback{Store: s, after: cancel}
			case "batch":
				store = &approvedBatch{Store: s, result: map[int64]int{1: 20}, after: cancel}
			}
			got, err := ReadApprovedCounts(ctx, store, []int64{1, 2, 3})
			if !errors.Is(err, context.Canceled) || got != nil {
				t.Fatal("cancelled counts escaped", err, got)
			}
		})
	}
	failure := errors.New("store unavailable")
	for _, store := range []Store{&approvedFallback{Store: s, err: failure}, &approvedBatch{Store: s, result: map[int64]int{1: 5}, err: failure}} {
		got, err := ReadApprovedCounts(t.Context(), store, []int64{1})
		if !errors.Is(err, failure) || got != nil {
			t.Fatal("outage became zero count", err, got)
		}
	}
	for _, ids := range [][]int64{{0}, {-1}, {1, 0}} {
		if got, err := ReadApprovedCounts(t.Context(), s, ids); !errors.Is(err, ErrInvalidInput) || got != nil {
			t.Fatal("invalid post IDs", got, err)
		}
		if got, err := s.CountApprovedByPost(t.Context(), ids); !errors.Is(err, ErrInvalidInput) || got != nil {
			t.Fatal("invalid direct post IDs", got, err)
		}
	}
	for _, result := range []map[int64]int{{1: -1}, {2: 5}} {
		if got, err := ReadApprovedCounts(t.Context(), &approvedBatch{Store: s, result: result}, []int64{1}); !errors.Is(err, ErrInvalidInput) || got != nil {
			t.Fatal("bad batch result accepted", got, err)
		}
	}
	provider := &approvedBatch{Store: s, result: map[int64]int{1: 4}}
	got, err := ReadApprovedCounts(t.Context(), provider, []int64{1, 2})
	if err != nil || got[2] != 0 {
		t.Fatal("missing result not zero", got, err)
	}
	got[1] = 999
	if provider.result[1] != 4 {
		t.Fatal("provider map mutated")
	}
}

func TestApprovedCountsConcurrentModeration(t *testing.T) {
	s := commentReadFixture(1000)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 200 {
			s.mu.Lock()
			c := s.items[1]
			if i%2 == 0 {
				c.Status = StatusPending
			} else {
				c.Status = StatusApproved
			}
			s.items[1] = c
			s.mu.Unlock()
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 100 {
				counts, err := ReadApprovedCounts(t.Context(), s, []int64{1, 2, 3, 4, 5, 6, 7})
				if err != nil {
					t.Error(err)
					return
				}
				for _, n := range counts {
					if n < 0 || n > 1000 {
						t.Error("invalid snapshot", counts)
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

func BenchmarkApprovedCounts(b *testing.B) {
	for _, size := range []int{1000, 10000} {
		store := commentReadFixture(size)
		ids := make([]int64, 20)
		for i := range ids {
			ids[i] = int64(i + 1)
		}
		for _, fallback := range []bool{true, false} {
			b.Run(fmt.Sprintf("comments=%d/fallback=%t", size, fallback), func(b *testing.B) {
				var source Store = store
				if fallback {
					source = struct{ Store }{store}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if _, err := ReadApprovedCounts(b.Context(), source, ids); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
