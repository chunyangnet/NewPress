package progression

import (
	"errors"
	"testing"
	"time"
)

func TestMembershipEditorDatesPlansReceiptsAndPersistence(t *testing.T) {
	s, _, cfg, now, path := fixture(t)
	in := MembershipInput{ActorID: 1, UserID: 2, ExpectedVersion: cfg.Version, Action: "set", Plan: "member", Since: now.Add(-time.Hour), Until: now.AddDate(0, 1, 0), RequestID: "membership-editor-set-1", Reason: "调整会员有效期"}
	result, err := s.Store.ManageMembership(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.Kind != "membership_set" || !result.Entry.Membership.Until.Equal(in.Until) {
		t.Fatal(result)
	}
	// A lost response must replay even after the membership version changes.
	replay, err := s.Store.ManageMembership(t.Context(), in)
	if err != nil || !replay.Replayed || replay.Entry.ID != result.Entry.ID {
		t.Fatalf("retry: %+v %v", replay, err)
	}
	in.RequestID, in.Plan = "membership-editor-set-2", "supporter"
	if _, err = s.Store.ManageMembership(t.Context(), in); !errors.Is(err, ErrMemberConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	in.MemberVersion = result.Entry.Membership.Version
	in.Until = now.AddDate(1, 0, 0)
	result, err = s.Store.ManageMembership(t.Context(), in)
	if err != nil || result.Entry.Membership.Plan != "supporter" {
		t.Fatalf("plan change: %+v %v", result, err)
	}
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Snapshot(t.Context(), 2, 0)
	if err != nil || snapshot.Account.Membership.Plan != "supporter" || !snapshot.Account.Membership.Until.Equal(in.Until) || len(snapshot.Entries) != 2 {
		t.Fatalf("reopened: %+v %v", snapshot, err)
	}
	in.MemberVersion, in.RequestID = result.Entry.Membership.Version, "membership-editor-bad-dates"
	in.Until = in.Since
	if _, err = reopened.ManageMembership(t.Context(), in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid dates: %v", err)
	}
	in.Until = now.AddDate(20, 0, 0)
	if _, err = reopened.ManageMembership(t.Context(), in); !errors.Is(err, ErrLimit) {
		t.Fatalf("unbounded membership: %v", err)
	}
}
