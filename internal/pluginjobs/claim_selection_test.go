package pluginjobs

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestClaimSelectionMatchesOrderedWalk(t *testing.T) {
	now := time.Date(2026, 11, 28, 3, 0, 0, 0, time.UTC)
	for seed := range uint64(24) {
		random := rand.New(rand.NewPCG(seed+1, 7))
		state := InitialState()
		for index := range 120 {
			input := testInput()
			input.Key = fmt.Sprintf("selection-%d", index)
			id, fingerprint, err := identity(input)
			if err != nil {
				t.Fatal(err)
			}
			job := newJob(input, id, fingerprint, now.Add(-time.Minute))
			job.Status = []string{"pending", "retry", "running", "succeeded", "failed", "cancelled"}[random.IntN(6)]
			job.Attempts = random.IntN(MaxAttempts + 1)
			job.AvailableAt = now.Add(time.Duration(random.IntN(7)-3) * time.Second)
			if job.Status == "running" {
				job.Attempts = max(1, job.Attempts)
				job.LeaseToken = strings.Repeat("b", 64)
				job.LeaseUntil = now.Add(time.Duration(random.IntN(5)-2) * time.Second)
			} else if random.IntN(5) == 0 {
				job.UpdatedAt = now.Add(time.Second)
			}
			// Exercise exhausted-only walks as well as walks with a winner.
			if seed%3 == 0 {
				job.Attempts = MaxAttempts
			}
			state.Jobs[id] = job
		}
		if err := ValidateState(state); err != nil {
			t.Fatal("invalid selection fixture", err)
		}
		backend, err := NewFileBackend("")
		if err != nil {
			t.Fatal(err)
		}
		backend.state = cloneState(state)
		queue := &Queue{Backend: backend}
		got, found, err := queue.Claim(t.Context(), now)
		if err != nil {
			t.Fatal(err)
		}
		// Oracle retains the previous full-sort walk, independently of the
		// production scans. Include all skipped jobs and partial expirations.
		ordered := make([]Job, 0, len(state.Jobs))
		for _, job := range state.Jobs {
			ordered = append(ordered, job)
		}
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].AvailableAt.Equal(ordered[j].AvailableAt) {
				return ordered[i].ID < ordered[j].ID
			}
			return ordered[i].AvailableAt.Before(ordered[j].AvailableAt)
		})
		wantedID := ""
		for _, job := range ordered {
			expired := job.Status == "running" && !job.LeaseUntil.After(now)
			if !expired && ((job.Status != "pending" && job.Status != "retry") || job.AvailableAt.After(now)) || now.Before(job.UpdatedAt) {
				continue
			}
			job.Version++
			job.UpdatedAt = now
			if job.Attempts >= MaxAttempts {
				job.Status, job.ErrorCode = "failed", "lease_expired"
				job.LeaseToken, job.LeaseUntil = "", time.Time{}
				state.Jobs[job.ID] = job
				continue
			}
			job.Status = "running"
			job.Attempts++
			job.LeaseUntil = now.Add(LeaseDuration)
			job.LeaseToken = got.LeaseToken
			state.Jobs[job.ID] = job
			wantedID = job.ID
			break
		}
		if got.ID != wantedID || found != (wantedID != "") || !reflect.DeepEqual(backend.state, state) {
			t.Fatalf("selection/expiration order changed at seed %d: found=%v got=%s want=%s", seed, found, got.ID, wantedID)
		}
	}
}
