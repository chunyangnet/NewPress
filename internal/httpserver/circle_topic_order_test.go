package httpserver

import (
	"slices"
	"testing"
	"time"

	"newpress.local/newpress/internal/community"
)

func TestCircleTopicOrderRespectsHiddenRepliesPinsAndTies(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	topics := []community.Topic{
		{ID: 1, CreatedAt: now, Pinned: true},
		{ID: 2, CreatedAt: now.Add(time.Minute)},
		{ID: 3, CreatedAt: now, Replies: []community.Reply{{Hidden: true, CreatedAt: now.Add(4 * time.Minute)}}},
		{ID: 4, CreatedAt: now, Replies: []community.Reply{{CreatedAt: now.Add(2 * time.Minute)}}},
		{ID: 5, CreatedAt: now.Add(2 * time.Minute)},
	}
	for _, tc := range []struct {
		name, order string
		manage      bool
		want        []int64
	}{
		{"reader activity", "active", false, []int64{1, 5, 4, 2, 3}},
		{"moderator activity", "active", true, []int64{1, 3, 5, 4, 2}},
		{"reader newest", "newest", false, []int64{1, 5, 4, 3, 2}},
		{"moderator default", "", true, []int64{1, 5, 4, 3, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := slices.Clone(topics)
			sortCircleTopics(got, tc.order, tc.manage)
			ids := make([]int64, len(got))
			for i := range got {
				ids[i] = got[i].ID
			}
			if !slices.Equal(ids, tc.want) {
				t.Fatalf("order %v, want %v", ids, tc.want)
			}
		})
	}
}

func BenchmarkCircleActiveTopicOrder(b *testing.B) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	topics := make([]community.Topic, community.MaxTopics)
	for i := range topics {
		topics[i] = community.Topic{ID: int64(i + 1), CreatedAt: now, Replies: make([]community.Reply, 10)}
		for j := range topics[i].Replies {
			topics[i].Replies[j] = community.Reply{Hidden: j == 9, CreatedAt: now.Add(time.Duration((i*317+j)%5000) * time.Minute)}
		}
	}
	work := make([]community.Topic, len(topics))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		copy(work, topics)
		sortCircleTopics(work, "active", false)
	}
}
