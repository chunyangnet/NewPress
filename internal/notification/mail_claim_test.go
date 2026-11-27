package notification

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"
)

func TestMailCompletionRejectsOldClaimAfterManualRetry(t *testing.T) {
	for _, outcome := range []string{"sent", "retry", "uncertain", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			store, path, now, delivery := notificationStorageFixture(t, true)
			if err := store.Project(t.Context(), delivery.Event.Key, []Delivery{delivery}, now); err != nil {
				t.Fatal(err)
			}
			var old Job
			for attempt := 1; attempt <= 8; attempt++ {
				claim, ok, err := store.ClaimMail(t.Context(), now)
				if err != nil || !ok || claim.Attempts != attempt {
					t.Fatalf("attempt %d: claim=%+v ok=%v err=%v", attempt, claim, ok, err)
				}
				if attempt == 1 {
					old = claim
				}
				if err := store.FinishMail(t.Context(), claim, "retry", now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				now = now.Add(24 * time.Hour)
			}
			if err := store.RetryFailed(t.Context(), delivery.UserID, now); err != nil {
				t.Fatal(err)
			}
			current, ok, err := store.ClaimMail(t.Context(), now)
			if err != nil || !ok || current.Attempts != old.Attempts {
				t.Fatalf("manual retry did not reuse the attempt count: %+v %v %v", current, ok, err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, completedAt := range []time.Time{old.UpdatedAt.Add(time.Second), now.Add(time.Second)} {
				if err := store.FinishMail(t.Context(), old, outcome, completedAt); !errors.Is(err, ErrConflict) {
					t.Fatalf("old %s completion changed the current claim: %v", outcome, err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected completion changed the durable queue: %v", err)
			}
			store, err = NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.FinishMail(t.Context(), current, "sent", now.Add(2*time.Second)); err != nil {
				t.Fatalf("current claim cannot finish after reload: %v", err)
			}
		})
	}
}

func TestMailCompletionRequiresCurrentClaim(t *testing.T) {
	for _, name := range []string{"state", "attempt", "missing-lease", "lease", "claimed-at", "before-claim"} {
		t.Run(name, func(t *testing.T) {
			store, path, now, delivery := notificationStorageFixture(t, true)
			if err := store.Project(t.Context(), delivery.Event.Key, []Delivery{delivery}, now); err != nil {
				t.Fatal(err)
			}
			current, ok, err := store.ClaimMail(t.Context(), now)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			claim, completedAt := current, now.Add(2*time.Second)
			switch name {
			case "state":
				claim.State = "pending"
			case "attempt":
				claim.Attempts++
			case "missing-lease":
				claim.LeaseUntil = nil
			case "lease":
				lease := claim.LeaseUntil.Add(time.Second)
				claim.LeaseUntil = &lease
			case "claimed-at":
				claim.UpdatedAt = claim.UpdatedAt.Add(time.Second)
			case "before-claim":
				completedAt = now.Add(-time.Second)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.FinishMail(t.Context(), claim, "sent", completedAt); !errors.Is(err, ErrConflict) {
				t.Fatalf("invalid claim completed: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected completion changed the durable queue: %v", err)
			}
			// Time zones and pointer identity are not part of a claim's identity.
			zone := time.FixedZone("reader", 8*60*60)
			current.UpdatedAt = current.UpdatedAt.In(zone)
			lease := current.LeaseUntil.In(zone)
			current.LeaseUntil = &lease
			if err := store.FinishMail(t.Context(), current, "sent", now.Add(3*time.Second)); err != nil {
				t.Fatalf("valid claim could not complete: %v", err)
			}
		})
	}
}

func TestMailCompletionRejectsExpiredLeaseBeforeRecovery(t *testing.T) {
	for _, outcome := range []string{"sent", "retry", "uncertain", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			store, _, now, delivery := notificationStorageFixture(t, false)
			if err := store.Project(t.Context(), delivery.Event.Key, []Delivery{delivery}, now); err != nil {
				t.Fatal(err)
			}
			claim, ok, err := store.ClaimMail(t.Context(), now)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			if err := store.FinishMail(t.Context(), claim, outcome, *claim.LeaseUntil); !errors.Is(err, ErrConflict) {
				t.Fatalf("expired lease completed as %s before recovery: %v", outcome, err)
			}
			if _, ok, err := store.ClaimMail(t.Context(), now.Add(2*time.Minute)); err != nil || ok {
				t.Fatalf("expired claim was resent: %v %v", ok, err)
			}
			status, err := store.MailStatus(t.Context(), delivery.UserID)
			if err != nil || status != (MailStatus{Uncertain: 1}) {
				t.Fatalf("expired claim lost uncertain state: %+v %v", status, err)
			}
		})
	}
}
