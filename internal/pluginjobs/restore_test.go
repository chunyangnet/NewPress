package pluginjobs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRestoreDoesNotMoveTaskClockBackward(t *testing.T) {
	q := testQueue(t, "")
	now := time.Now().UTC()
	input := Input{Plugin: "fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "future-clock", Payload: json.RawMessage(`{}`)}
	if _, err := q.Enqueue(t.Context(), input, now); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := q.Claim(t.Context(), now.Add(time.Hour))
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = q.CancelRestored(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	state, err := q.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if job := state.Jobs[claim.ID]; job.Status != "cancelled" || job.UpdatedAt.Before(claim.UpdatedAt) {
		t.Fatal("restore regressed clock", job.Status, job.UpdatedAt)
	}
}

func TestRestoreFencesClaimsAndRetainsReplayReceipt(t *testing.T) {
	backend, err := NewFileBackend(t.TempDir() + "/tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	q := &Queue{Backend: backend}
	now := time.Now().UTC()
	in := Input{Plugin: "fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "stable", ActorID: 1, Payload: json.RawMessage(`{}`)}
	if _, err = q.Enqueue(t.Context(), in, now); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := q.Claim(t.Context(), now)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = q.CancelRestored(t.Context(), time.Time{}); err != ErrInvalid {
		t.Fatalf("zero restoration time accepted: %v", err)
	}
	if err = q.CheckClaim(t.Context(), claim, now); err != nil {
		t.Fatalf("invalid restoration changed claim: %v", err)
	}
	if err = q.CancelRestored(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = q.Finish(t.Context(), claim, "", now.Add(2*time.Second)); err != ErrConflict {
		t.Fatalf("old claim accepted: %v", err)
	}
	replay, err := q.Enqueue(t.Context(), in, now.Add(3*time.Second))
	if err != nil || replay.Status != "cancelled" {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if _, ok, err = q.Claim(t.Context(), now.Add(time.Hour)); err != nil || ok {
		t.Fatalf("restored work resumed: %v %v", ok, err)
	}
}
