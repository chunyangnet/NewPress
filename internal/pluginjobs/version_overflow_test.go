package pluginjobs

import (
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionOverflowPreservesDurableTask(t *testing.T) {
	for _, action := range []string{"claim", "cancel", "restore", "finish", "retry"} {
		t.Run(action, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.json")
			q := testQueue(t, path)
			now := time.Now().UTC()
			input := Input{Plugin: "overflow", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "stable", ActorID: 1, Payload: json.RawMessage(`{}`)}
			job, err := q.Enqueue(t.Context(), input, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := q.Backend.Update(t.Context(), func(s *State) error {
				job.Version = math.MaxInt64
				if action == "finish" {
					job.Status = "running"
					job.Attempts = 1
					job.LeaseToken = strings.Repeat("b", 64)
					job.LeaseUntil = now.Add(LeaseDuration)
				}
				if action == "retry" {
					job.Status = "failed"
				}
				s.Jobs[job.ID] = job
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before, err := q.Backend.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "claim":
				var claimed Job
				var ok bool
				claimed, ok, err = q.Claim(t.Context(), now)
				if ok || claimed.ID != "" || claimed.LeaseToken != "" {
					t.Fatal("uncommitted lease escaped")
				}
			case "cancel", "retry":
				err = q.Change(t.Context(), job.Plugin, job.ID, job.Version, action, now)
			case "restore":
				err = q.CancelRestored(t.Context(), now)
			case "finish":
				err = q.Finish(t.Context(), job, "", now)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("overflow error = %v", err)
			}
			for _, store := range []*Queue{q, testQueue(t, path)} {
				after, err := store.Backend.Read(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				a, _ := json.Marshal(after)
				b, _ := json.Marshal(before)
				if string(a) != string(b) {
					t.Fatal("overflow changed persisted or in-memory state")
				}
			}
		})
	}
}
