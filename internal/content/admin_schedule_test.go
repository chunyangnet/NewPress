package content

import (
	"context"
	"testing"
	"time"
)

func TestAdminScheduleWindowBeforePagination(t *testing.T) {
	now := time.Now().UTC()
	r := NewMemoryRepository(false)
	for i := 12; i >= 1; i-- {
		_, err := r.Create(context.Background(), PostInput{Title: "Scheduled " + time.Duration(i).String(), Type: TypePost, Status: StatusFuture, AuthorID: 1, PublishedAt: now.Add(time.Duration(i) * 24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := r.ListAdmin(context.Background(), AdminQuery{Status: StatusFuture, AuthorID: 1, PublishedAfter: now, PublishedBefore: now.Add(7 * 24 * time.Hour), PublicationAscending: true, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 7 || len(result.Items) != 5 {
		t.Fatalf("total=%d items=%d", result.Total, len(result.Items))
	}
	for i, p := range result.Items {
		if !p.PublishedAt.Equal(now.Add(time.Duration(i+1) * 24 * time.Hour)) {
			t.Fatalf("unexpected order %v", p.PublishedAt)
		}
	}
	result, err = r.ListAdmin(context.Background(), AdminQuery{AuthorID: 2, Status: StatusFuture, PublishedAfter: now, PublishedBefore: now.Add(7 * 24 * time.Hour), PublicationAscending: true, Limit: 5})
	if err != nil || result.Total != 0 {
		t.Fatalf("author scope: %+v %v", result, err)
	}
}
