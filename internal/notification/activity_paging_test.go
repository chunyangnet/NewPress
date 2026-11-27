package notification

import (
	"context"
	"errors"
	"newpress.local/newpress/internal/activity"
	"testing"
	"time"
)

type pagedIsolationSource struct{ isolatedActivitySource }

func (s *pagedIsolationSource) PendingActivityAfter(ctx context.Context, now time.Time, limit int, after string) ([]activity.Event, error) {
	return activity.PendingAfter(s.events, now, limit, after), ctx.Err()
}

func TestActivityRetryProbePreservesForwardProgress(t *testing.T) {
	now := time.Now().UTC()
	source := &pagedIsolationSource{}
	for i := int64(1); i <= 700; i++ {
		source.events = append(source.events, activity.New(activity.ContentUpdated, i, i, 1, 1, "draft", now))
	}
	calls := map[string]int{}
	failure := errors.New("all observers fail")
	service := &Service{Sources: []activity.Source{source}, BeforeAck: func(_ context.Context, e activity.Event) error {
		calls[e.Key]++
		return failure
	}}
	for range 10 {
		if err := service.Tick(t.Context(), now); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
	cursor := service.activityCursors[0]
	if len(calls) != 500 {
		t.Fatal("scan did not advance", len(calls))
	}
	if err := service.Tick(t.Context(), now); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	first := activity.PendingAfter(source.events, now, 50, "")
	if calls[first[0].Key] != 2 || service.activityCursors[0] != cursor {
		t.Fatal("probe lost cursor or skipped old failures")
	}
	if err := service.Tick(t.Context(), now); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(calls) != 550 || len(source.events) != 700 {
		t.Fatal("probe starved later pages or acknowledged failures")
	}
}

func TestFailedFullPageDoesNotStarveLaterActivity(t *testing.T) {
	now := time.Now().UTC()
	source := &pagedIsolationSource{}
	for i := int64(1); i <= 120; i++ {
		source.events = append(source.events, activity.New(activity.ContentUpdated, i, i, 1, 1, "draft", now))
	}
	first := activity.PendingAfter(source.events, now, 50, "")
	poison := map[string]bool{}
	for _, event := range first {
		poison[event.Key] = true
	}
	failure := errors.New("observer unavailable")
	service := &Service{Sources: []activity.Source{source}, BeforeAck: func(_ context.Context, e activity.Event) error {
		if poison[e.Key] {
			return failure
		}
		return nil
	}}
	if err := service.Tick(t.Context(), now); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(source.events) != 120 {
		t.Fatal("failed page acknowledged")
	}
	for range 2 {
		if err := service.Tick(t.Context(), now); err != nil {
			t.Fatal(err)
		}
	}
	if len(source.events) != 50 {
		t.Fatal("later events starved", len(source.events))
	}
	// Wrap-around retries the failed page without requiring a process restart.
	if err := service.Tick(t.Context(), now); !errors.Is(err, failure) {
		t.Fatal("failed page was not retried", err)
	}
	if len(source.events) != 50 {
		t.Fatal("retry acknowledged failed events")
	}
	// A new worker has no scheduling hints, but must retry all durable failures.
	service = &Service{Sources: []activity.Source{source}}
	if err := service.Tick(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if len(source.events) != 0 {
		t.Fatal("restart lost failed events")
	}
}
