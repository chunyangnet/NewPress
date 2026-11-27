package pluginjobs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestClaimAuthorizationTracksCancellation(t *testing.T) {
	b, err := NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	q := &Queue{Backend: b}
	now := time.Now().UTC()
	_, err = q.Enqueue(t.Context(), Input{Plugin: "fixture", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "claim", ActorID: 1, Payload: json.RawMessage(`{}`)}, now)
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := q.Claim(t.Context(), now)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = q.CheckClaim(t.Context(), claim, now); err != nil {
		t.Fatal(err)
	}
	forged := claim
	forged.ActorID = 2
	if err = q.CheckClaim(t.Context(), forged, now); err != ErrAuthorization {
		t.Fatal("forged actor", err)
	}
	for _, mutate := range []func(*Job){
		func(j *Job) { j.Kind = "event.deliver.v1" },
		func(j *Job) { j.Key = "different-key" },
		func(j *Job) { j.Payload = json.RawMessage(`{"changed":true}`) },
		func(j *Job) { j.LeaseUntil = j.LeaseUntil.Add(time.Minute) },
	} {
		forged = claim
		mutate(&forged)
		if err = q.CheckClaim(t.Context(), forged, now); err != ErrAuthorization {
			t.Fatal("changed claimed request accepted", err)
		}
	}
	if err = q.Change(t.Context(), claim.Plugin, claim.ID, claim.Version, "cancel", now); err != nil {
		t.Fatal(err)
	}
	if err = q.CheckClaim(t.Context(), claim, now); err != ErrAuthorization {
		t.Fatal("cancelled claim", err)
	}
}
