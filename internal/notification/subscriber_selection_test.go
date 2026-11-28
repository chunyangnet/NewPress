package notification

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"
)

func subscriberFixture(count int) *FileStore {
	s := &FileStore{state: emptyDocument()}
	at := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	for id := int64(1); id <= int64(count); id++ {
		p := Defaults(id)
		p.Version, p.UpdatedAt, p.WeeklySince = 1, at, at.AddDate(0, 0, -7)
		p.UnsubscribeKey, p.Weekly = fmt.Sprintf("%064x", id), id%4 != 0
		s.state.Preferences[id] = p
	}
	return s
}

func orderedSubscribers(s *FileStore, after int64, limit int) []Preferences {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	items := []Preferences{}
	for id, p := range s.state.Preferences {
		if id > after && p.Weekly {
			items = append(items, p)
		}
	}
	slices.SortFunc(items, func(a, b Preferences) int {
		if a.UserID < b.UserID {
			return -1
		}
		if a.UserID > b.UserID {
			return 1
		}
		return 0
	})
	return items[:min(len(items), limit)]
}

func TestSubscribersMatchOrderedSnapshot(t *testing.T) {
	s := subscriberFixture(1003)
	if err := validateDocument(s.state); err != nil {
		t.Fatal(err)
	}
	for _, after := range []int64{-1, 0, 1, 399, 1000, 1003, math.MaxInt64} {
		for _, limit := range []int{-1, 0, 1, 17, 100, 200, 201, math.MaxInt} {
			t.Run(fmt.Sprintf("after-%d/limit-%d", after, limit), func(t *testing.T) {
				got, err := s.Subscribers(t.Context(), after, limit)
				want := orderedSubscribers(s, after, limit)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("page differs from ordered snapshot: got=%v want=%v error=%v", got, want, err)
				}
				if len(got) > 0 {
					id := got[0].UserID
					got[0].Weekly = false
					if !s.state.Preferences[id].Weekly {
						t.Fatal("caller changed stored subscription")
					}
				}
			})
		}
	}
	var after int64
	seen := []Preferences{}
	for {
		page, err := s.Subscribers(t.Context(), after, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		seen = append(seen, page...)
		after = page[len(page)-1].UserID
	}
	want := []Preferences{}
	for id := int64(1); id <= 1003; id++ {
		if p := s.state.Preferences[id]; p.Weekly {
			want = append(want, p)
		}
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatal("paging repeated or omitted subscribers")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if page, err := s.Subscribers(ctx, 0, 100); !errors.Is(err, context.Canceled) || len(page) != 0 {
		t.Fatalf("cancelled listing returned data: count=%d error=%v", len(page), err)
	}
}

func BenchmarkSubscribersPage(b *testing.B) {
	for _, count := range []int{10000, 100000} {
		s := subscriberFixture(count)
		for _, position := range []struct {
			name  string
			after int64
		}{{"first", 0}, {"middle", int64(count / 2)}, {"tail", int64(count - 80)}} {
			b.Run(fmt.Sprintf("%d/%s", count, position.name), func(b *testing.B) {
				ctx := context.Background()
				want := len(orderedSubscribers(s, position.after, 100))
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					page, err := s.Subscribers(ctx, position.after, 100)
					if err != nil || len(page) != want {
						b.Fatal(len(page), want, err)
					}
				}
			})
		}
	}
}
