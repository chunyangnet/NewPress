package resources

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPurchaseIdentityPreservesOriginalTermsAcrossRepricing(t *testing.T) {
	r := example().Items[0]
	v := r.Variants[0]
	c := v.Groups[0].Conditions[0]
	key, legacy := PurchaseKey(1, r, v, c), ConditionKey(1, r, v, c)
	c.Amount, c.Currency = 1990, "USD"
	if PurchaseKey(1, r, v, c) != key || ConditionKey(1, r, v, c) == legacy {
		t.Fatal("price/currency confused with purchase identity")
	}
	terms := Terms(1, r, v, c)
	if terms.Key() != key || terms.LegacyKey() != ConditionKey(1, r, v, c) {
		t.Fatal("inconsistent snapshot keys")
	}
	for _, changed := range []PurchaseTerms{
		{PostID: 2, Resource: r.ID, Variant: v.ID, Condition: c.ID, Kind: c.Kind},
		{PostID: 1, Resource: "another", Variant: v.ID, Condition: c.ID, Kind: c.Kind},
		{PostID: 1, Resource: r.ID, Variant: "another", Condition: c.ID, Kind: c.Kind},
		{PostID: 1, Resource: r.ID, Variant: v.ID, Condition: "another", Kind: c.Kind},
		{PostID: 1, Resource: r.ID, Variant: v.ID, Condition: c.ID, Kind: "points"},
	} {
		if changed.Key() == key {
			t.Fatal("different identity reused purchase", changed)
		}
	}
}

func TestPointReservationConcurrencyCancellationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	backend, err := NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{Backend: backend}
	ends := time.Now().Add(time.Hour).UTC()
	in := PointPurchase{UserID: 2, Terms: PurchaseTerms{PostID: 1, Resource: "bundle", Variant: "full", Condition: "points", Kind: "points", Currency: "main", Amount: 100, EndsAt: &ends}, CurrencyVersion: 1, Precision: 1, Title: "资源", CreatedAt: time.Now().UTC()}
	var wg sync.WaitGroup
	requests := make(chan string, 24)
	for range 24 {
		wg.Go(func() {
			p, err := store.BeginPointPurchase(context.Background(), in)
			if err != nil {
				t.Error(err)
				return
			}
			requests <- p.Payment().RequestID
		})
	}
	wg.Wait()
	close(requests)
	first, err := store.BeginPointPurchase(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	for request := range requests {
		if request != first.Payment().RequestID {
			t.Fatal("multiple debit reservations")
		}
	}
	in.Terms.Currency, in.Terms.Amount = "bonus", 200
	pinned, err := store.BeginPointPurchase(t.Context(), in)
	if err != nil || pinned.Terms.Currency != "main" || pinned.Terms.Amount != 100 {
		t.Fatal("pending debit replaced before cancellation", pinned, err)
	}
	ack := PointReceipt{ID: 1, UserID: 2, Amount: 100, RequestID: first.Payment().RequestID, CreatedAt: time.Now().UTC()}
	if _, err = store.FinishPointPurchase(t.Context(), first, ack); err != nil {
		t.Fatal("deadline snapshot did not survive JSON cloning", err)
	}
	next, err := store.BeginPointPurchase(t.Context(), in)
	if err != nil || next.Attempt != 2 || next.Terms.Amount != 200 || next.Payment().RequestID == first.Payment().RequestID {
		t.Fatal("sealed cancellation cannot retry", next, err)
	}
	ack.Paid = true
	if _, err = store.FinishPointPurchase(t.Context(), first, ack); !errors.Is(err, ErrPurchaseChanged) {
		t.Fatal("stale receipt overwrote a new attempt", err)
	}
	ack.ID, ack.Amount, ack.RequestID = 2, 200, next.Payment().RequestID
	if _, err = store.FinishPointPurchase(t.Context(), next, ack); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := reopened.Read(t.Context())
	if err != nil || state.Purchases[next.Key()].PaymentEntry != 2 || len(state.Unlocks) != 1 {
		t.Fatal("purchase projection lost", state, err)
	}
	if ValidateState(InitialState()) != nil {
		t.Fatal("legacy schema without purchase snapshots rejected")
	}
}
