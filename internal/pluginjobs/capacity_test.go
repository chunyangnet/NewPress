package pluginjobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Build a legal, almost-full durable queue in one pass. Completed receipts fill
// the file but are not runnable; selected tasks exercise the remaining space.
func capacityFixture(t *testing.T, targetBytes int, jobs ...Job) (State, []byte) {
	t.Helper()
	state := InitialState()
	for _, job := range jobs {
		state.Jobs[job.ID] = job
	}
	return capacityStateFixture(t, targetBytes, state)
}

func capacityStateFixture(t *testing.T, targetBytes int, state State) (State, []byte) {
	t.Helper()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	size := len(encoded)
	for index := 0; ; index++ {
		in := testInput()
		in.Plugin = fmt.Sprintf("capacity-%d", index/900)
		in.Key = fmt.Sprintf("filler-%d", index)
		in.Payload = json.RawMessage(`"` + strings.Repeat("x", MaxPayloadBytes-2) + `"`)
		job := capacityJob(t, in, "succeeded")
		raw, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		// The map key has a fixed 64-byte identity; quotes, colon and comma add 4.
		added := len(raw) + len(job.ID) + 4
		if len(state.Jobs) == 0 {
			added--
		}
		if size+added > targetBytes {
			remaining := targetBytes - size
			if remaining < added-(MaxPayloadBytes-2) {
				// Shrink an earlier full payload to make room for one more row.
				for id, existing := range state.Jobs {
					if existing.Status == "succeeded" && len(existing.Payload) == MaxPayloadBytes {
						delete(state.Jobs, id)
						in = Input{Plugin: existing.Plugin, Epoch: existing.Epoch, Kind: existing.Kind, Key: existing.Key, Payload: json.RawMessage(`"` + strings.Repeat("x", MaxPayloadBytes-1026) + `"`)}
						existing = capacityJob(t, in, "succeeded")
						state.Jobs[existing.ID] = existing
						size -= 1024
						break
					}
				}
				continue
			}
			in.Payload = json.RawMessage(`"` + strings.Repeat("x", MaxPayloadBytes-2-(size+added-targetBytes)) + `"`)
			job = capacityJob(t, in, "succeeded")
			state.Jobs[job.ID] = job
			break
		}
		state.Jobs[job.ID] = job
		size += added
		if size == targetBytes {
			break
		}
	}
	if err := ValidateState(state); err != nil {
		t.Fatal("invalid capacity fixture", err)
	}
	encoded, err = json.Marshal(state)
	if err != nil || len(encoded) != targetBytes {
		t.Fatalf("bad size fixture: bytes=%d want=%d err=%v", len(encoded), targetBytes, err)
	}
	return state, encoded
}

func capacityJob(t *testing.T, in Input, status string) Job {
	t.Helper()
	id, fingerprint, err := identity(in)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	return Job{ID: id, Plugin: in.Plugin, Epoch: in.Epoch, Kind: in.Kind, Key: in.Key, Fingerprint: fingerprint, ActorID: in.ActorID, Payload: in.Payload, Status: status, Version: 1, CreatedAt: now, UpdatedAt: now, AvailableAt: now}
}

func TestFileQueueReservesSpaceBeforeAdmission(t *testing.T) {
	in := testInput()
	job := capacityJob(t, in, "pending")
	state, _ := capacityFixture(t, MaxStateBytes-1, job)
	delete(state.Jobs, job.ID)
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "jobs.json")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	queue := testQueue(t, path)
	got, err := queue.Enqueue(t.Context(), in, job.CreatedAt)
	if !errors.Is(err, ErrCapacity) || got.ID != "" {
		t.Errorf("accepted a task without room for its lease: id=%q err=%v", got.ID, err)
		_, ok, claimErr := queue.Claim(t.Context(), job.CreatedAt)
		t.Logf("execution after admission: claimed=%v err=%v", ok, claimErr)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected admission changed durable receipts", err)
	}
	queue = testQueue(t, path)
	var existing Job
	for _, existing = range state.Jobs {
		break
	}
	replayInput := Input{Plugin: existing.Plugin, Epoch: existing.Epoch, Kind: existing.Kind, Key: existing.Key, Payload: existing.Payload}
	if replay, err := queue.Enqueue(t.Context(), replayInput, job.CreatedAt); err != nil || replay.Status != "succeeded" {
		t.Fatal("capacity prevented an existing receipt replay", err)
	}
}

func TestFileQueueKeepsAdmittedWorkExecutableAtCapacity(t *testing.T) {
	in := testInput()
	job := capacityJob(t, in, "pending")
	state, _ := capacityFixture(t, MaxStateBytes-300, job)
	delete(state.Jobs, job.ID)
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "jobs.json")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	queue := testQueue(t, path)
	if _, err := queue.Enqueue(t.Context(), in, job.CreatedAt); err != nil {
		t.Fatal("queue with enough execution space rejected a task", err)
	}
	queue = testQueue(t, path)
	now := job.CreatedAt.Add(123456789 * time.Nanosecond)
	claim, ok, err := queue.Claim(t.Context(), now)
	if err != nil || !ok {
		t.Fatal("accepted work cannot acquire its lease", err)
	}
	// A new frozen event must not consume the running task's completion space.
	event := Event{Key: "new-event", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	if receipt, err := queue.FreezeFanout(t.Context(), event, nil); !errors.Is(err, ErrCapacity) || receipt.Event.Key != "" {
		t.Fatal("event admission consumed reserved task space", err)
	}
	if err := queue.Finish(t.Context(), claim, "execution_failed", now); err != nil {
		t.Fatal("accepted work cannot save retry metadata", err)
	}
	queue = testQueue(t, path)
	now = now.Add(3 * time.Second)
	claim, ok, err = queue.Claim(t.Context(), now)
	if err != nil || !ok || claim.Attempts != 2 {
		t.Fatal("accepted work cannot retry", err)
	}
	if err := queue.Finish(t.Context(), claim, "", now); err != nil {
		t.Fatal("accepted work cannot save completion", err)
	}
	queue = testQueue(t, path)
	replay, err := queue.Enqueue(t.Context(), in, now)
	if err != nil || replay.ID != job.ID || replay.Status != "succeeded" {
		t.Fatal("completed receipt could not replay at capacity", err)
	}
}

func TestFileQueueReservesSpaceForRestoredFanouts(t *testing.T) {
	required := len(`,"delivered":true`) + fanoutCancellationReserveBytes
	for _, spare := range []int{required - 1, required} {
		t.Run(fmt.Sprint(spare), func(t *testing.T) {
			event := Event{Key: "backup", Kind: "content.updated", ObjectID: 1, Version: 1, At: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)}
			state := InitialState()
			state.Fanouts = map[string]FanoutReceipt{event.Key: {Event: event, Subscriptions: []Subscription{}}}
			state, _ = capacityStateFixture(t, MaxStateBytes-spare, state)
			delete(state.Fanouts, event.Key)
			before, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "jobs.json")
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			queue := testQueue(t, path)
			receipt, err := queue.FreezeFanout(t.Context(), event, nil)
			if spare < required {
				if !errors.Is(err, ErrCapacity) || receipt.Event.Key != "" {
					t.Fatal("receipt was admitted without restore space", err)
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("failed freeze changed durable state", err)
				}
				return
			}
			if err != nil {
				t.Fatal("exact restore boundary was rejected", err)
			}
			if err := queue.CompleteFanout(t.Context(), event); err != nil {
				t.Fatal("frozen receipt cannot persist delivery", err)
			}
			queue = testQueue(t, path)
			if err := queue.CancelRestored(t.Context(), event.At.Add(time.Second)); err != nil {
				t.Fatal("frozen receipt cannot be safely restored", err)
			}
			queue = testQueue(t, path)
			receipt, err = queue.FreezeFanout(t.Context(), event, nil)
			if err != nil || !receipt.Cancelled {
				t.Fatal("restore cancellation did not persist", err)
			}
			body, err := os.ReadFile(path)
			if err != nil || len(body) != MaxStateBytes {
				t.Fatal("incorrect exact-boundary fixture", len(body), err)
			}
		})
	}
}

func TestFailedTaskKeepsRoomForManualRetry(t *testing.T) {
	in := testInput()
	job := capacityJob(t, in, "failed")
	job.Attempts = MaxAttempts
	job.ErrorCode = "lease_expired"
	state, encoded := capacityFixture(t, MaxStateBytes-300, job)
	path := filepath.Join(t.TempDir(), "jobs.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	queue := testQueue(t, path)
	now := job.CreatedAt.Add(987654321 * time.Nanosecond)
	if err := queue.Change(t.Context(), job.Plugin, job.ID, job.Version, "retry", now); err != nil {
		t.Fatal("failed task cannot be manually retried", err)
	}
	claim, ok, err := queue.Claim(t.Context(), now)
	if err != nil || !ok || claim.Attempts != 1 {
		t.Fatal("manual retry cannot execute", err)
	}
	if err := queue.CancelRestored(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal("restoring an active retry exceeded capacity", err)
	}
	queue = testQueue(t, path)
	restored, err := queue.Enqueue(t.Context(), in, now)
	if err != nil || restored.Status != "cancelled" || restored.ErrorCode != "authorization_changed" {
		t.Fatal("restore failed to fence retried work", err)
	}
	stateAfter, err := queue.Backend.Read(t.Context())
	if err != nil || len(stateAfter.Jobs) != len(state.Jobs) {
		t.Fatal("restore lost durable receipts", err)
	}
}
