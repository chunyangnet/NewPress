package aijobs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestProPriorityAndRetrySurviveRestart(t *testing.T) {
	q, m := testQueue(t)
	st := snapshot(t, q)
	now := time.Now().UTC()
	r := Rules{Author: "editor", UserID: 6, Language: "auto", RetryLimit: 2}
	j := Job{Topic: "短文", Author: r.Author, UserID: 6, Work: WorkSpec{Kind: "enrich", Key: "enrich/1", Rules: r}}
	urgent := j
	urgent.Priority = 8
	urgent.Work.Key = "comment/9"
	if err := q.AddWork(st.Epoch, []Job{j, urgent}, now); err != nil {
		t.Fatal(err)
	}
	ran, err := q.step(t.Context(), now, func(ctx context.Context, job Job) (string, error) {
		if job.Work.Key != urgent.Work.Key {
			t.Fatal("priority ignored")
		}
		return "", ErrTransient
	})
	if !ran || err != nil {
		t.Fatal(err)
	}
	state := snapshot(t, q)
	retry := state.Jobs[1]
	if retry.Status != "queued" || retry.Due.Before(now.Add(59*time.Second)) {
		t.Fatalf("wrong retry: %+v", retry)
	}
	q = New(m)
	_, err = q.step(t.Context(), now.Add(2*time.Minute), func(ctx context.Context, job Job) (string, error) {
		if job.ID != retry.ID || job.Attempts != 2 {
			t.Fatalf("wrong retry after restart: %+v", job)
		}
		return "完成", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.step(t.Context(), now.Add(2*time.Minute), func(ctx context.Context, job Job) (string, error) {
		return "完成", nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestProSchedulePauseResumeAndEnd(t *testing.T) {
	q, _ := testQueue(t)
	st := snapshot(t, q)
	now := time.Now().UTC()
	p := Schedule{ID: "recurring-plan-example", Brief: "主题", Author: "worker", Model: "text", Language: "auto", Hours: 24, Remaining: 2, Next: now, Destination: "pending"}
	if err := q.Schedule(st.Epoch, p, false); err != nil {
		t.Fatal(err)
	}
	if err := q.tickSchedules(now); err != nil {
		t.Fatal(err)
	}
	if err := q.PauseSchedule(st.Epoch, p.ID, true); err != nil {
		t.Fatal(err)
	}
	if ran, _ := q.step(t.Context(), now, func(context.Context, Job) (string, error) { t.Fatal("paused plan ran"); return "", nil }); ran {
		t.Fatal("claimed paused job")
	}
	_ = q.PauseSchedule(st.Epoch, p.ID, false)
	_, err := q.step(t.Context(), now, func(ctx context.Context, j Job) (string, error) {
		if err := q.StoreTitles(ctx, j, []string{"下一个具体标题"}); err != nil {
			t.Fatal(err)
		}
		return "已创建", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.tickSchedules(now.Add(25 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	state := snapshot(t, q)
	if len(state.Jobs) != 2 || state.Jobs[1].Work.PlannedTitle != "下一个具体标题" {
		t.Fatal(state.Jobs)
	}
	if err = q.Schedule(st.Epoch, p, true); err != nil {
		t.Fatal(err)
	}
	state = snapshot(t, q)
	if state.Jobs[1].Status != "cancelled" || len(state.Schedules) != 0 {
		t.Fatal(state)
	}
	if err := q.Action(st.Epoch, state.Jobs[1].ID, state.Jobs[1].Version, "retry", now); !errors.Is(err, ErrConflict) {
		t.Fatal("retry accepted for an ended schedule", err)
	}
	if !errors.Is(q.PauseSchedule(st.Epoch, p.ID, true), ErrNotFound) {
		t.Fatal("removed plan resurrected")
	}
}

func TestProManualRetryRestoresAutomaticRetryBudget(t *testing.T) {
	q, _ := testQueue(t)
	st := snapshot(t, q)
	now := time.Now().UTC()
	j := Job{Topic: "重试", Author: "editor", Work: WorkSpec{Kind: "enrich", Key: "retry-budget", Rules: Rules{RetryLimit: 1}}}
	if err := q.AddWork(st.Epoch, []Job{j}, now); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := q.step(t.Context(), now.Add(time.Duration(i)*time.Hour), func(context.Context, Job) (string, error) { return "", ErrTransient }); err != nil {
			t.Fatal(err)
		}
	}
	failed := snapshot(t, q).Jobs[0]
	if failed.Status != "failed" || failed.Attempts != 2 {
		t.Fatal(failed)
	}
	if err := q.Action(st.Epoch, failed.ID, failed.Version, "retry", now); err != nil {
		t.Fatal(err)
	}
	if _, err := q.step(t.Context(), now, func(context.Context, Job) (string, error) { return "", ErrTransient }); err != nil {
		t.Fatal(err)
	}
	retry := snapshot(t, q).Jobs[0]
	if retry.Status != "queued" || retry.Attempts != 1 || !retry.Due.After(now) {
		t.Fatalf("manual retry lost its retry budget: %+v", retry)
	}
}

func TestProSchedulesUseRemainingCapacityWithoutLosingTitles(t *testing.T) {
	q, m := testQueue(t)
	st := snapshot(t, q)
	now := time.Now().UTC()
	jobs := make([]Job, MaxJobs-1)
	for i := range jobs {
		jobs[i] = Job{Author: "editor", Due: now.Add(48 * time.Hour), Work: WorkSpec{Kind: "enrich", Key: fmt.Sprintf("capacity/%d", i)}}
	}
	if err := q.AddWork(st.Epoch, jobs, now); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		p := Schedule{ID: fmt.Sprintf("capacity-schedule-%d", i), Brief: "方向", Author: "editor", Language: "auto", Hours: 24, Remaining: 2, Next: now, Destination: "pending", Titles: []string{fmt.Sprintf("标题 %d", i)}}
		if err := q.Schedule(st.Epoch, p, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.tickSchedules(now); err != nil {
		t.Fatal(err)
	}
	q = New(m)
	state := snapshot(t, q)
	if len(state.Jobs) != MaxJobs || state.Jobs[MaxJobs-1].Work.PlannedTitle != "标题 0" || state.Schedules[0].Remaining != 1 || state.Schedules[1].Remaining != 2 || len(state.Schedules[1].Titles) != 1 || !state.Schedules[1].Next.Equal(now) {
		t.Fatalf("partial schedule progress not persisted: %+v", state.Schedules)
	}
	if _, err := q.step(t.Context(), now, func(context.Context, Job) (string, error) { return "已创建", nil }); err != nil {
		t.Fatal(err)
	}
	if err := q.tickSchedules(now); err != nil {
		t.Fatal(err)
	}
	state = snapshot(t, q)
	if len(state.Jobs) != MaxJobs || state.Jobs[MaxJobs-1].Work.PlannedTitle != "标题 1" || state.Schedules[1].Remaining != 1 || len(state.Schedules[1].Titles) != 0 {
		t.Fatal("second schedule did not resume after capacity became available", state.Schedules)
	}
}

func TestProExternalResultsSurviveQueueRotationUntilCollected(t *testing.T) {
	now := time.Now().UTC()
	s := State{Jobs: make([]Job, MaxJobs)}
	for i := range s.Jobs {
		s.Jobs[i] = Job{Status: "queued", Work: WorkSpec{Kind: "enrich", Key: fmt.Sprintf("queued/%d", i)}}
	}
	s.Jobs[0] = Job{Status: "succeeded", Updated: now, Work: WorkSpec{Kind: "external", Key: "external/result"}, Result: "{}"}
	newJob := Job{Author: "editor", Work: WorkSpec{Kind: "enrich", Key: "incoming"}}
	if _, err := appendWork(&s, newJob, now.Add(time.Hour)); !errors.Is(err, ErrFull) {
		t.Fatal("uncollected bridge result was evicted", err)
	}
	collected := State{Jobs: append([]Job(nil), s.Jobs...)}
	collected.Jobs[0].Work.Collected = true
	if _, err := appendWork(&collected, newJob, now.Add(time.Hour)); err != nil {
		t.Fatal("collected result still blocks the queue", err)
	}
	if _, err := appendWork(&s, newJob, now.Add(24*time.Hour)); err != nil || s.Jobs[0].Work.Kind == "external" {
		t.Fatal("expired bridge result did not release capacity", err)
	}
}
