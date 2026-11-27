package pluginjobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileArchiveHistoryCountRefusesReclamationWithoutEviction(t *testing.T) {
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	j := capacityJob(t, testInput(), "cancelled")
	base, err := ArchiveJob(j, now)
	if err != nil {
		t.Fatal(err)
	}
	base.Plugin, base.Kind = "a", "b"
	state := InitialState()
	state.Jobs[j.ID] = j
	state.ArchivedJobs = map[string]ArchivedJob{}
	for i := range MaxArchivedJobs {
		item := base
		item.ID = hash([]byte(fmt.Sprintf("archived-count-%d", i)))
		state.ArchivedJobs[item.ID] = item
	}
	if err := ValidateState(state); err != nil {
		t.Fatal("invalid count fixture", err)
	}
	if err := checkStorageSize(state, false); err != nil {
		t.Fatal("count fixture also exceeded byte budget", err)
	}
	b := &FileBackend{state: state}
	q := &Queue{Backend: b}
	if result, err := q.Archive(t.Context(), ArchiveQuery{Before: now, Tasks: true}, now); !errors.Is(err, ErrCapacity) || result != (ArchiveResult{}) {
		t.Fatal("archive evicted history to reclaim capacity", result, err)
	}
	if len(b.state.ArchivedJobs) != MaxArchivedJobs || len(b.state.Jobs) != 1 {
		t.Fatal("failed archive changed identity history")
	}
}

func TestFileArchiveByteBudgetRollbackAndRestoreReservation(t *testing.T) {
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	in := testInput()
	in.Plugin, in.Kind = strings.Repeat("a", 96), strings.Repeat("b", 96)
	j := capacityJob(t, in, "cancelled")
	baseJob, err := ArchiveJob(j, now)
	if err != nil {
		t.Fatal(err)
	}
	baseJob.Plugin, baseJob.Kind = "a", "b"
	subs := make([]Subscription, 1000)
	for i := range subs {
		subs[i] = Subscription{Plugin: fmt.Sprintf("reader-%04d-", i) + strings.Repeat("a", 84), Epoch: strings.Repeat("b", 64), Since: now, ActorID: 1}
	}
	makeFanout := func(i int) ArchivedFanout {
		return ArchivedFanout{Receipt: FanoutReceipt{Event: Event{Key: fmt.Sprintf("fill-%06d", i), Kind: "content.updated", ObjectID: 1, Version: 1, At: now}, Subscriptions: subs, Delivered: true}, ArchivedAt: now}
	}
	state := InitialState()
	state.Jobs[j.ID] = j
	state.ArchivedFanouts = map[string]ArchivedFanout{}
	first := makeFanout(0)
	state.ArchivedFanouts[first.Receipt.Event.Key] = first
	historyBytes := func(jobs map[string]ArchivedJob, fanouts map[string]ArchivedFanout) int {
		raw, err := json.Marshal(struct {
			Jobs    map[string]ArchivedJob    `json:"archived_jobs,omitempty"`
			Fanouts map[string]ArchivedFanout `json:"archived_fanouts,omitempty"`
		}{jobs, fanouts})
		if err != nil {
			t.Fatal(err)
		}
		return len(raw)
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	addedFanout := len(raw) + len(first.Receipt.Event.Key) + 4 + fanoutCancellationReserveBytes
	required := historyBytes(nil, state.ArchivedFanouts) + fanoutCancellationReserveBytes
	for i := 1; required+addedFanout <= MaxArchiveBytes; i++ {
		f := makeFanout(i)
		state.ArchivedFanouts[f.Receipt.Event.Key] = f
		required += addedFanout
	}
	state.ArchivedJobs = map[string]ArchivedJob{}
	baseJob.ID = hash([]byte("budget-0"))
	firstJobAdded := historyBytes(map[string]ArchivedJob{baseJob.ID: baseJob}, map[string]ArchivedFanout{first.Receipt.Event.Key: first}) - historyBytes(nil, map[string]ArchivedFanout{first.Receipt.Event.Key: first})
	for i := 0; ; i++ {
		item := baseJob
		item.ID = hash([]byte(fmt.Sprintf("budget-%d", i)))
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		added := len(raw) + len(item.ID) + 4
		if i == 0 {
			added = firstJobAdded
		}
		if required+added > MaxArchiveBytes {
			break
		}
		state.ArchivedJobs[item.ID] = item
		required += added
	}
	actual := historyBytes(state.ArchivedJobs, state.ArchivedFanouts) + len(state.ArchivedFanouts)*fanoutCancellationReserveBytes
	if required != actual || MaxArchiveBytes-required > 600 {
		t.Fatalf("bad archive byte fixture: measured=%d tracked=%d remaining=%d", actual, required, MaxArchiveBytes-required)
	}
	if err := ValidateState(state); err != nil {
		t.Fatal("invalid byte fixture", err)
	}
	if err := checkStorageSize(state, false); err != nil {
		t.Fatal("fixture already exceeded archive budget", err)
	}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "jobs.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	q := testQueue(t, path)
	if result, err := q.Archive(t.Context(), ArchiveQuery{Tasks: true, Before: now}, now); !errors.Is(err, ErrCapacity) || result != (ArchiveResult{}) {
		t.Fatal("byte overflow was not rolled back", result, err)
	}
	q = testQueue(t, path)
	if replay, err := q.Enqueue(t.Context(), in, now); err != nil || replay.Status != "cancelled" || !replay.ArchivedAt.IsZero() {
		t.Fatal("failed archive removed original receipt", replay, err)
	}
	if err := q.CancelRestored(t.Context(), now); err != nil {
		t.Fatal("history budget did not reserve restore cancellation", err)
	}
	q = testQueue(t, path)
	restored, err := q.Backend.Read(t.Context())
	if err != nil || len(restored.ArchivedFanouts) != len(state.ArchivedFanouts) {
		t.Fatal("restore lost archived event identities", err)
	}
	for _, f := range restored.ArchivedFanouts {
		if !f.Receipt.Cancelled || !f.Receipt.Delivered || len(f.Receipt.Subscriptions) != 1000 {
			t.Fatal("restore lost terminal facts or frozen cohort")
		}
	}
}
