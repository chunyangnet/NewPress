package pluginjobs

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestClaimDoesNotExposeStoredReview(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint("disk=", disk), func(t *testing.T) {
			path := ""
			if disk {
				path = filepath.Join(t.TempDir(), "tasks.json")
			}
			q := testQueue(t, path)
			now := time.Now().UTC()
			_, request := cancelledReviewSource(t, q, now)
			child, err := q.Reissue(t.Context(), request, now.Add(2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			claim, found, err := q.Claim(t.Context(), now.Add(3*time.Second))
			if err != nil || !found || claim.ID != child.ID || claim.Review == nil {
				t.Fatalf("claim reissued job: %v, found=%v", err, found)
			}
			original := claim
			original.Review = CloneReview(claim.Review)
			claim.Review.Reason = "executor modified its own copy"
			claim.Review.ReviewerID = 999
			state, err := q.Backend.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(state.Jobs[claim.ID].Review, original.Review) {
				t.Error("claim exposes the stored review pointer")
			}
			if err = ValidateState(state); err != nil {
				t.Error("caller mutation invalidated the queue:", err)
			}
			if err = q.Finish(t.Context(), original, "", now.Add(4*time.Second)); err != nil {
				t.Fatal("caller mutation blocked durable completion:", err)
			}
			if disk {
				q = testQueue(t, path)
			}
			state, err = q.Backend.Read(t.Context())
			if err != nil || state.Jobs[claim.ID].Status != "succeeded" || !reflect.DeepEqual(state.Jobs[claim.ID].Review, original.Review) {
				t.Fatal("completion lost the original review", err)
			}
		})
	}
}
