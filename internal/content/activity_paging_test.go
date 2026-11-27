package content

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestActivityPagingSurvivesFileReopenAndACK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	store, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		_, err := store.Create(t.Context(), PostInput{AuthorID: 2, Title: "Paging", Slug: fmt.Sprintf("paging-%d", i), Status: StatusPublish})
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Add(time.Second)
	first, err := store.PendingActivityAfter(t.Context(), now, 1, "")
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	if err := store.AckActivity(t.Context(), first[0].Key); err != nil {
		t.Fatal(err)
	}
	store, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := store.PendingActivityAfter(t.Context(), now, 50, first[0].Key)
	if err != nil || len(rest) != 2 {
		t.Fatal("ACK shifted cursor", rest, err)
	}
	all, err := store.PendingActivityAfter(t.Context(), now, 50, "")
	if err != nil || len(all) != 2 || all[0].Key != rest[0].Key {
		t.Fatal("restart lost pending events", all, err)
	}
}
