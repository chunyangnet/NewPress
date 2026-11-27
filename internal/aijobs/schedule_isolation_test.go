package aijobs

import (
	"testing"
	"time"
)

func TestScheduleOwnsItsTitlePool(t *testing.T) {
	q, manager := testQueue(t)
	state := snapshot(t, q)
	now := time.Now().UTC()
	p := Schedule{ID: "isolated-plan-example", Brief: "主题", Author: "worker", Model: "text", Language: "auto", Hours: 24, Remaining: 2, Next: now, Destination: "pending", Titles: []string{"原始标题", "后续标题"}}
	if err := q.Schedule(state.Epoch, p, false); err != nil {
		t.Fatal(err)
	}
	p.Titles[0] = "调用方修改"
	if got := snapshot(t, q).Schedules[0].Titles[0]; got != "原始标题" {
		t.Errorf("caller changed queued title: %q", got)
	}
	// An unrelated save must not make the caller's change durable.
	if err := q.PauseSchedule(state.Epoch, p.ID, true); err != nil {
		t.Fatal(err)
	}
	reopened := New(manager)
	if got := snapshot(t, reopened).Schedules[0].Titles[0]; got != "原始标题" {
		t.Errorf("caller changed persisted title: %q", got)
	}
	if err := q.PauseSchedule(state.Epoch, p.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := q.tickSchedules(now); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, q).Jobs[0].Work.PlannedTitle; got != "原始标题" {
		t.Errorf("worker received caller's changed title: %q", got)
	}
}
