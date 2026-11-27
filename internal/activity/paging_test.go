package activity

import (
	"fmt"
	"testing"
	"time"
)

func TestPendingAfterBoundedStableAndDetached(t *testing.T) {
	now := time.Now().UTC()
	var events []Event
	for i := 120; i > 0; i-- {
		events = append(events, Event{Key: fmt.Sprintf("key:%03d", i), Due: now, Mentions: []string{"original"}})
	}
	events = append(events, Event{Key: "key:000", Due: now.Add(time.Hour)})
	page := PendingAfter(events, now, 50, "")
	if len(page) != 50 || page[0].Key != "key:001" || page[49].Key != "key:050" {
		t.Fatal("bad first page", page)
	}
	page[0].Mentions[0] = "changed"
	if events[119].Mentions[0] != "original" || events[0].Key != "key:120" {
		t.Fatal("source mutated")
	}
	for _, event := range page {
		events = Without(events, event.Key)
	}
	next := PendingAfter(events, now, 50, page[49].Key)
	if len(next) != 50 || next[0].Key != "key:051" || next[49].Key != "key:100" {
		t.Fatal("ACK skipped rows", next)
	}
	last := PendingAfter(events, now, 50, next[49].Key)
	if len(last) != 20 || last[19].Key != "key:120" {
		t.Fatal("bad last page", last)
	}
}
