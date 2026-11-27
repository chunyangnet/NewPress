package aijobs

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
)

func testQueue(t *testing.T) (*Queue, *plugin.Manager) {
	t.Helper()
	m := plugin.NewManager(t.TempDir())
	enable(t, m)
	return New(m), m
}
func enable(t *testing.T, m *plugin.Manager) {
	t.Helper()
	if err := m.InstallBundled(plugin.AIProMax); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(plugin.AIProMax, true); err != nil {
		t.Fatal(err)
	}
}
func snapshot(t *testing.T, q *Queue) State {
	t.Helper()
	st, err := q.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return st
}
func plan() Plan {
	return Plan{Key: "writing-plan-012345", Author: "editor", UserID: 3, Provider: "model", Model: "writer", PostType: "post", Language: "zh-CN", Topics: []string{"第一篇", "第二篇"}, IntervalMinutes: 10}
}

func TestScheduledPlanAndSubmissionReceipt(t *testing.T) {
	q, _ := testQueue(t)
	now := time.Now().UTC()
	before := snapshot(t, q)
	p := plan()
	p.Start = now.Add(time.Hour)
	if err := q.Enqueue(before.Epoch, p, now); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(before.Epoch, p, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	st := snapshot(t, q)
	if len(st.Jobs) != 2 || !st.Jobs[1].Due.Equal(p.Start.Add(10*time.Minute)) {
		t.Fatal(st)
	}
	p.Language = "en"
	if err := q.Enqueue(before.Epoch, p, now); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	p.Language = "zh-CN"
	calls := 0
	generate := func(context.Context, Job) (string, error) { calls++; return "正文", nil }
	step := func(at time.Time, want bool) {
		t.Helper()
		ran, err := q.step(t.Context(), at, generate)
		if ran != want || err != nil {
			t.Fatal(ran, err)
		}
	}
	step(now, false)
	if err := q.Action(st.Epoch, "", st.ControlVersion, "pause", now); err != nil {
		t.Fatal(err)
	}
	step(st.Jobs[0].Due, false)
	if err := q.Action(st.Epoch, "", st.ControlVersion, "resume", now); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := q.Action(st.Epoch, "", snapshot(t, q).ControlVersion, "resume", now); err != nil {
		t.Fatal(err)
	}
	step(p.Start, true)
	step(p.Start, false)
	if calls != 1 {
		t.Fatal(calls)
	}
	if err := q.Action(st.Epoch, st.Jobs[1].ID, 1, "advance", now); err != nil {
		t.Fatal(err)
	}
	step(now, true)
	for _, j := range snapshot(t, q).Jobs {
		if j.Status != "succeeded" || j.Result != "正文" || j.Attempts != 1 {
			t.Fatal(j)
		}
		if err := q.Action(st.Epoch, j.ID, j.Version, "remove", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Enqueue(st.Epoch, p, now); err != nil || len(snapshot(t, q).Jobs) != 0 {
		t.Fatal("replayed deleted plan", err)
	}
}

func TestWorkerLateCompletionCannotReviveCancelledOrReinstalledJob(t *testing.T) {
	for _, op := range []string{"cancel", "disable", "uninstall"} {
		t.Run(op, func(t *testing.T) {
			q, m := testQueue(t)
			epoch := snapshot(t, q).Epoch
			p := plan()
			p.Topics = p.Topics[:1]
			now := time.Now()
			if err := q.Enqueue(epoch, p, now); err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			exit := make(chan error, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			go func() {
				_, err := q.step(ctx, now, func(c context.Context, _ Job) (string, error) {
					close(started)
					<-c.Done()
					return "迟到的结果", nil
				})
				exit <- err
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			j := snapshot(t, q).Jobs[0]
			if j.Status != "running" {
				t.Fatal(j)
			}
			switch op {
			case "cancel":
				if err := q.Action(epoch, j.ID, j.Version-1, "cancel", now); !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
				if err := q.Action(epoch, j.ID, j.Version, "cancel", now); err != nil {
					t.Fatal(err)
				}
			case "disable":
				if err := m.SetEnabled(plugin.AIProMax, false); err != nil {
					t.Fatal(err)
				}
				if err := m.SetEnabled(plugin.AIProMax, true); err != nil {
					t.Fatal(err)
				}
			case "uninstall":
				if err := m.Uninstall(plugin.AIProMax); err != nil {
					t.Fatal(err)
				}
				enable(t, m)
			}
			select {
			case err := <-exit:
				if op == "cancel" && err != nil || op != "cancel" && !errors.Is(err, plugin.ErrInactive) {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("worker did not stop")
			}
			st := snapshot(t, q)
			if op == "uninstall" {
				if len(st.Jobs) != 0 {
					t.Fatal(st)
				}
				if err := q.Enqueue(epoch, p, now); !errors.Is(err, ErrConflict) {
					t.Fatal("stale installation", err)
				}
				return
			}
			want := "cancelled"
			if op == "disable" {
				want = "interrupted"
			}
			j = st.Jobs[0]
			if j.Status != want || j.Result != "" {
				t.Fatal(j)
			}
			if err := q.Action(st.Epoch, j.ID, j.Version, "retry", now); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRestartRecoversAbandonedClaimWithoutRebilling(t *testing.T) {
	q, m := testQueue(t)
	p := plan()
	p.Topics = p.Topics[:1]
	now := time.Now()
	if err := q.Enqueue(snapshot(t, q).Epoch, p, now); err != nil {
		t.Fatal(err)
	}
	err := m.WithData(plugin.AIProMax, true, 0, func(dir string, _ uint64) error {
		path := filepath.Join(dir, "writing-queue.json")
		st, err := plugin.ReadPrivate[State](path, maxStateBytes)
		if err != nil {
			return err
		}
		st.Jobs[0].Status = "running"
		st.Jobs[0].Version++
		st.Jobs[0].Attempts = 1
		return plugin.WritePrivate(path, st)
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened := New(m)
	if ran, err := reopened.step(t.Context(), now, func(context.Context, Job) (string, error) { t.Fatal("unrequested billable retry"); return "", nil }); ran || err != nil {
		t.Fatal(ran, err)
	}
	j := snapshot(t, reopened).Jobs[0]
	if j.Status != "interrupted" || j.Attempts != 1 || j.Problem == "" {
		t.Fatal(j)
	}
}

func TestBusyWorkerAndOversizedResult(t *testing.T) {
	q, _ := testQueue(t)
	p := plan()
	p.Topics = p.Topics[:1]
	now := time.Now()
	if err := q.Enqueue(snapshot(t, q).Epoch, p, now); err != nil {
		t.Fatal(err)
	}
	if _, err := q.step(t.Context(), now, func(context.Context, Job) (string, error) { return "", ErrBusy }); err != nil {
		t.Fatal(err)
	}
	j := snapshot(t, q).Jobs[0]
	if j.Status != "queued" || j.Attempts != 0 {
		t.Fatal(j)
	}
	if _, err := q.step(t.Context(), j.Due, func(context.Context, Job) (string, error) { return strings.Repeat("<", MaxResult), nil }); err != nil {
		t.Fatal(err)
	}
	j = snapshot(t, q).Jobs[0]
	if j.Status != "failed" || j.Problem != ErrOutput.Error() {
		t.Fatal(j)
	}
}
