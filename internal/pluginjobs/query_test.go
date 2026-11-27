package pluginjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestListScopePaginationAndRedaction(t *testing.T) {
	ctx := context.Background()
	q := testQueue(t, "")
	now := time.Now().UTC()
	for i := range 4 {
		in := testInput()
		in.Key = fmt.Sprintf("private-key-%d", i)
		in.ActorID = 999
		in.Payload = json.RawMessage(`{"secret":"never-export"}`)
		if i == 3 {
			in.Plugin = "other"
		}
		if _, err := q.Enqueue(ctx, in, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := q.List(ctx, Query{Plugin: "example", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Items) != 2 || !page.Items[0].CreatedAt.Equal(now.Add(2*time.Second)) {
		t.Fatalf("bad first page: %+v", page)
	}
	next, err := q.List(ctx, Query{Plugin: "example", Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if next.Total != 3 || len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID {
		t.Fatal("bad pagination")
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"payload", "epoch", "fingerprint", "lease_token", "actor_id", "private-key", "never-export"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("leaked %s", private)
		}
	}
	empty, err := q.List(ctx, Query{Plugin: "example", Status: "failed"})
	if err != nil || empty.Total != 0 || empty.Items == nil {
		t.Fatalf("empty filter: %+v %v", empty, err)
	}
	for _, query := range []Query{{Plugin: ""}, {Plugin: "example", Status: "unknown"}, {Plugin: "example", Offset: -1}, {Plugin: "example", Limit: 101}} {
		if _, err = q.List(ctx, query); err == nil {
			t.Fatal("accepted invalid query")
		}
	}
}
