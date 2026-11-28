package resources

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func resourceSnapshotFixture() State {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	ends := now.Add(time.Hour)
	s := InitialState()
	s.Submissions[Hash("submission")] = Submission{Actor: "u:1", PostID: 1, Resource: "bundle", Variant: "full", Fields: map[string]string{"email": "reader@example.test"}, UpdatedAt: now}
	s.Deliveries = []Delivery{{Actor: "u:1", PostID: 1, Resource: "bundle", Variant: "full", Link: "file", Request: "download", CreatedAt: now}}
	s.Unlocks = []Unlock{{Actor: "u:1", PostID: 1, Resource: "bundle", Variant: "full", CreatedAt: now}}
	p := PointPurchase{UserID: 1, Terms: PurchaseTerms{PostID: 1, Resource: "bundle", Variant: "full", Condition: "points", Kind: "points", Currency: "main", Amount: 100, EndsAt: &ends}, Attempt: 1, CurrencyVersion: 1, Status: "pending", CreatedAt: now}
	s.Purchases = map[string]PointPurchase{p.Key(): p}
	return s
}

func mutateResourceSnapshot(s *State) {
	for key, sub := range s.Submissions {
		sub.Fields["email"] = "changed@example.test"
		delete(s.Submissions, key)
	}
	s.Deliveries[0].Actor = "u:99"
	s.Unlocks[0].Actor = "u:99"
	for key, p := range s.Purchases {
		*p.Terms.EndsAt = p.Terms.EndsAt.Add(24 * time.Hour)
		delete(s.Purchases, key)
	}
}

func TestResourceSnapshotsIsolateReadsWritesAndAbortedTransactions(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			path := ""
			if mode == "file" {
				path = filepath.Join(t.TempDir(), "resources.json")
			}
			backend, err := NewFileBackend(path)
			if err != nil {
				t.Fatal(err)
			}
			input, want := resourceSnapshotFixture(), resourceSnapshotFixture()
			if err := backend.Update(t.Context(), func(s *State) error { *s = input; return nil }); err != nil {
				t.Fatal(err)
			}
			mutateResourceSnapshot(&input)
			assertSnapshot := func() State {
				t.Helper()
				got, err := backend.Read(t.Context())
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("snapshot changed: %+v, %v", got, err)
				}
				return got
			}
			read := assertSnapshot()
			mutateResourceSnapshot(&read)
			assertSnapshot()
			aborted := errors.New("abort")
			if err := backend.Update(t.Context(), func(s *State) error { mutateResourceSnapshot(s); return aborted }); !errors.Is(err, aborted) {
				t.Fatal(err)
			}
			assertSnapshot()
			if path != "" {
				backend, err = NewFileBackend(path)
				if err != nil {
					t.Fatal(err)
				}
				assertSnapshot()
			}
		})
	}
}

func BenchmarkResourceSnapshotRead(b *testing.B) {
	backend, _ := NewFileBackend("")
	state := resourceSnapshotFixture()
	for i := range 10000 {
		d := state.Deliveries[0]
		d.Request = fmt.Sprintf("request-%d", i)
		state.Deliveries = append(state.Deliveries, d)
	}
	if err := backend.Update(context.Background(), func(s *State) error { *s = state; return nil }); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		got, err := backend.Read(context.Background())
		if err != nil || len(got.Deliveries) != len(state.Deliveries) {
			b.Fatal(err)
		}
	}
}
