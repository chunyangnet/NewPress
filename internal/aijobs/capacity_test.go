package aijobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
)

func capacityResult(t *testing.T) string {
	t.Helper()
	result := strings.Repeat("<", 16383) + "text"
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) != 96<<10 || !ValidResult(result) || ValidResult(result+"x") {
		t.Fatal("fixture must be a valid result at the encoded size boundary")
	}
	return result
}

func queueBytes(t *testing.T, q *Queue) []byte {
	t.Helper()
	var data []byte
	err := q.m.WithData(plugin.AIProMax, true, 0, func(dir string, _ uint64) error {
		var err error
		data, err = os.ReadFile(filepath.Join(dir, "writing-queue.json"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestQueueCapacityStopsBeforeBillingAndResumesAfterRemoval(t *testing.T) {
	q, m := testQueue(t)
	epoch := snapshot(t, q).Epoch
	rules := Rules{Author: "editor", Language: "zh-CN", BlockWords: strings.Repeat("<", 8192), Persona: strings.Repeat("<", 2048), WakeWords: strings.Repeat("<", 512)}
	if err := q.SaveRules(epoch, rules); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for batch := range MaxJobs / MaxTopics {
		p := plan()
		p.Key = fmt.Sprintf("capacity-plan-%04d", batch)
		p.IntervalMinutes = 0
		p.Topics = make([]string, MaxTopics)
		for i := range p.Topics {
			p.Topics[i] = fmt.Sprintf("article %d", batch*MaxTopics+i)
		}
		if err := q.Enqueue(epoch, p, now); err != nil {
			t.Fatal(err)
		}
	}
	result := capacityResult(t)
	calls, completed := 0, 0
	generate := func(context.Context, Job) (string, error) {
		calls++
		return result, nil
	}
	for completed < MaxJobs {
		before := queueBytes(t, q)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		ran, err := q.step(ctx, now, generate)
		cancel()
		if errors.Is(err, ErrFull) {
			if ran || calls != completed || !bytes.Equal(before, queueBytes(t, q)) {
				t.Fatal("capacity rejection billed work or changed durable state")
			}
			break
		}
		if err != nil || !ran {
			t.Fatalf("completion %d: ran=%v calls=%d err=%v", completed+1, ran, calls, err)
		}
		completed++
	}
	if completed == 0 || completed == MaxJobs {
		t.Fatalf("fixture did not reach the byte limit: completed=%d", completed)
	}
	state := snapshot(t, New(m))
	if state.Jobs[completed].Status != "queued" || state.Jobs[completed].Version != 1 || state.Jobs[completed].Attempts != 0 {
		t.Fatal("capacity rejection persisted a claim")
	}
	for _, j := range state.Jobs[:completed] {
		if j.Status != "succeeded" || j.Result != result {
			t.Fatal("completed result lost on restart")
		}
	}
	first := state.Jobs[0]
	if err := q.Action(epoch, first.ID, first.Version, "remove", now); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if ran, err := q.step(ctx, now, generate); !ran || err != nil {
		t.Fatalf("freed capacity did not resume: ran=%v err=%v", ran, err)
	}
	state = snapshot(t, New(m))
	if len(state.Jobs) != MaxJobs-1 || state.Jobs[completed-1].Status != "succeeded" || state.Jobs[completed-1].Result != result || calls != completed+1 {
		t.Fatal("resume lost or regenerated a result")
	}
	t.Logf("accepted=%d completed_before_capacity=%d model_calls=%d durable_bytes=%d", MaxJobs, completed, calls, len(queueBytes(t, q)))
}

func TestRunningJobKeepsResultSpaceDuringOtherQueueWrites(t *testing.T) {
	q, m := testQueue(t)
	state := snapshot(t, q)
	now := time.Date(2026, 11, 20, 0, 0, 0, 123456789, time.UTC)
	job := Job{Author: "editor", Work: WorkSpec{Kind: "external", Key: "capacity/concurrent"}}
	candidate := state
	if _, err := appendWork(&candidate, job, now); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	// AddWork accepts opaque source snapshots. Leave enough space to claim and
	// finish, but not enough for another write to consume the result allowance.
	job.Work.Source = strings.Repeat("x", maxStateBytes-(96<<10)-1024-512-len(encoded))
	if err := q.AddWork(state.Epoch, []Job{job}, now); err != nil {
		t.Fatal(err)
	}
	result := capacityResult(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ran, err := q.step(ctx, now, func(context.Context, Job) (string, error) {
		before := queueBytes(t, q)
		rules := Rules{Author: "editor", Language: "auto", Persona: strings.Repeat("<", 512)}
		if err := q.SaveRules(state.Epoch, rules); !errors.Is(err, ErrFull) {
			t.Errorf("rule update consumed reserved result space: %v", err)
		}
		extra := Job{Author: "editor", Work: WorkSpec{Kind: "enrich", Key: "capacity/incoming", Source: strings.Repeat("x", 2048)}}
		if err := q.AddWork(state.Epoch, []Job{extra}, now); !errors.Is(err, ErrFull) {
			t.Errorf("incoming work consumed reserved result space: %v", err)
		}
		if !bytes.Equal(before, queueBytes(t, q)) || snapshot(t, q).Rules.Version != state.Rules.Version {
			t.Error("rejected update changed disk or in-memory rules")
		}
		return result, nil
	})
	if !ran || err != nil {
		t.Fatalf("reserved result did not persist: ran=%v err=%v", ran, err)
	}
	reopened := snapshot(t, New(m))
	if len(reopened.Jobs) != 1 || reopened.Jobs[0].Status != "succeeded" || reopened.Jobs[0].Result != result {
		t.Fatal("completed result was not durable")
	}
}

func TestRecoveryCannotWriteAnUnreadableQueue(t *testing.T) {
	q, m := testQueue(t)
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	state := State{Schema: 1, ControlVersion: 1, Instance: "capacity-recovery-instance", Jobs: []Job{{Work: WorkSpec{Source: "x"}, ID: "capacity-recovery-0001", Version: 9, Attempts: 1, Status: "running", Due: now, Updated: now}}}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	state.Jobs[0].Work.Source = strings.Repeat("x", 1+maxStateBytes-len(encoded))
	var path string
	if err := m.WithData(plugin.AIProMax, true, 0, func(dir string, _ uint64) error {
		path = filepath.Join(dir, "writing-queue.json")
		return plugin.WritePrivate(path, state)
	}); err != nil {
		t.Fatal(err)
	}
	before := queueBytes(t, q)
	if len(before) != maxStateBytes {
		t.Fatal("fixture must be an old queue at the exact read limit")
	}
	if _, err := q.Snapshot(); !errors.Is(err, ErrFull) {
		t.Fatalf("recovery should reject an oversized rewrite: %v", err)
	}
	if !bytes.Equal(before, queueBytes(t, q)) {
		t.Fatal("failed recovery overwrote the original queue")
	}
	if _, err := plugin.ReadPrivate[State](path, maxStateBytes); err != nil {
		t.Fatalf("failed recovery made the file unreadable: %v", err)
	}
}

func TestQueueCapacityRetainsOneHundredOrdinaryResults(t *testing.T) {
	q, m := testQueue(t)
	epoch := snapshot(t, q).Epoch
	now := time.Now().UTC()
	for batch := range MaxJobs / MaxTopics {
		p := plan()
		p.Key = fmt.Sprintf("ordinary-plan-%04d", batch)
		p.IntervalMinutes = 0
		p.Topics = make([]string, MaxTopics)
		for i := range p.Topics {
			p.Topics[i] = fmt.Sprintf("article %d", batch*MaxTopics+i)
		}
		if err := q.Enqueue(epoch, p, now); err != nil {
			t.Fatal(err)
		}
	}
	result := capacityResult(t)
	calls := 0
	for i := range MaxJobs {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		ran, err := q.step(ctx, now, func(context.Context, Job) (string, error) {
			calls++
			return result, nil
		})
		cancel()
		if !ran || err != nil {
			t.Fatalf("ordinary result %d: ran=%v err=%v", i+1, ran, err)
		}
	}
	state := snapshot(t, New(m))
	if len(state.Jobs) != MaxJobs || calls != MaxJobs {
		t.Fatal("ordinary queue capacity was reduced")
	}
	for _, job := range state.Jobs {
		if job.Status != "succeeded" || job.Result != result {
			t.Fatal("ordinary result lost")
		}
	}
}
