package pluginjobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type failingRecipientStore struct {
	Store
	plugin string
}

func (s failingRecipientStore) Enqueue(ctx context.Context, in Input, now time.Time) (Job, error) {
	if in.Plugin == s.plugin {
		return Job{}, errors.New("temporary persistence failure")
	}
	return s.Store.Enqueue(ctx, in, now)
}
func TestFanoutPartialFailureReplaysWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	q := testQueue(t, "")
	now := time.Now().UTC()
	event := Event{Key: "update:1:1", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	subscriptions := []Subscription{
		{Plugin: "first", Epoch: strings.Repeat("a", 64), Since: now},
		{Plugin: "second", Epoch: strings.Repeat("b", 64), Since: now},
		{Plugin: "third", Epoch: strings.Repeat("c", 64), Since: now},
	}
	if err := Fanout(ctx, failingRecipientStore{Store: q, plugin: "second"}, event, subscriptions, now); err == nil {
		t.Fatal("source would be acknowledged after failure")
	}
	state, err := q.Backend.Read(ctx)
	if err != nil || len(state.Jobs) != 2 {
		t.Fatal("failure blocked independent recipients", err)
	}
	if err = Fanout(ctx, q, event, subscriptions, now); err != nil {
		t.Fatal(err)
	}
	state, err = q.Backend.Read(ctx)
	if err != nil || len(state.Jobs) != 3 {
		t.Fatal("replay lost or duplicated tasks", err)
	}
}

func TestFanoutReplayAndSubscriptionBoundary(t *testing.T) {
	ctx := context.Background()
	q := testQueue(t, "")
	now := time.Now().UTC()
	event := Event{Key: "publication:1:2", Kind: "content.published", ObjectID: 1, Version: 2, At: now}
	subscriptions := []Subscription{
		{Plugin: "first", Epoch: strings.Repeat("a", 64), Since: now.Add(-time.Hour)},
		{Plugin: "later", Epoch: strings.Repeat("b", 64), Since: now.Add(time.Second)},
	}
	for range 2 {
		if err := Fanout(ctx, q, event, subscriptions, now); err != nil {
			t.Fatal(err)
		}
	}
	s, err := q.Backend.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Jobs) != 1 {
		t.Fatalf("expected one unique recipient, got %d", len(s.Jobs))
	}
	for _, j := range s.Jobs {
		if j.Plugin != "first" || j.Kind != "event.deliver.v1" {
			t.Fatal("incorrect recipient")
		}
	}
	event.Version++
	if err = Fanout(ctx, q, event, subscriptions, now); err == nil {
		t.Fatal("changed source event replay accepted")
	}
}
func TestFanoutValidatesAllRecipientsBeforeWriting(t *testing.T) {
	q := testQueue(t, "")
	now := time.Now().UTC()
	err := Fanout(context.Background(), q, Event{Key: "key", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}, []Subscription{
		{Plugin: "valid", Epoch: strings.Repeat("a", 64), Since: now},
		{Plugin: "invalid", Epoch: "bad", Since: now},
	}, now)
	if err == nil {
		t.Fatal("accepted invalid recipient")
	}
	s, err := q.Backend.Read(context.Background())
	if err != nil || len(s.Jobs) != 0 {
		t.Fatal("partial write before validation", err)
	}
}
