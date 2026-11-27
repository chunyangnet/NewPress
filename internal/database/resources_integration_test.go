package database

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/payment"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/virtualcurrency"
)

func TestPostgresResourceQuotaPersistenceAndPrivateMetadata(t *testing.T) {
	coin, _, ids := currencyPostgres(t)
	backend := NewResourceBackend(coin.pool)
	store := &resources.Store{Backend: backend}
	now := time.Now().UTC()
	var success atomic.Int32
	var wg sync.WaitGroup
	r := resources.Resource{ID: "resource", DailyLimit: 3}
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := store.Consume(context.Background(), resources.Delivery{Actor: fmt.Sprintf("u:%d", ids[0]), PostID: 1, Resource: r.ID, Variant: "full", Link: "file", Request: fmt.Sprintf("request-%d", i), Day: "2026-11-21", CreatedAt: now}, r)
			if e == nil {
				success.Add(1)
			} else if e != resources.ErrLimit {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 3 {
		t.Fatal("concurrent quota", success.Load())
	}
	state, e := NewResourceBackend(coin.pool).Read(t.Context())
	if e != nil || len(state.Deliveries) != 3 {
		t.Fatal(state, e)
	}
	if replay, e := store.Consume(t.Context(), state.Deliveries[0], r); e != nil || !replay {
		t.Fatal(replay, e)
	}
	cfg := resources.Config{Items: []resources.Resource{{ID: "private", Title: "隐藏资源", Variants: []resources.Variant{{ID: "full", Name: "完整内容", Kind: "content", Body: `<p>SECRET_PG</p><img src="/media/42/private.png">`, Groups: []resources.Group{{Conditions: []resources.Condition{{ID: "login", Kind: "login"}}}}}}}}}
	repo := NewPostRepository(coin.pool)
	post, e := repo.Create(t.Context(), content.PostInput{AuthorID: ids[0], Title: "资源元数据", ContentSource: "公开内容", Resources: &cfg})
	if e != nil {
		t.Fatal(e)
	}
	restored, e := repo.GetByID(t.Context(), post.ID)
	if e != nil || len(resources.FromMeta(restored.Meta).Items) != 1 || strings.Contains(restored.ContentHTML, "SECRET_PG") {
		t.Fatal("metadata roundtrip", e)
	}
	found := false
	if e = repo.WalkMediaSnapshots(t.Context(), func(s content.MediaSnapshot) error {
		if s.PostID == post.ID && strings.Contains(s.ContentHTML, "/media/42/private.png") {
			found = true
		}
		return nil
	}); e != nil || !found {
		t.Fatal("private media reference missing", e)
	}
}
func TestPostgresResourcePaymentVerifiedAndIdempotent(t *testing.T) {
	service, _, _, ids := commercePostgres(t)
	in := commerce.ResourcePayment{UserID: ids[0], Key: resources.Hash("private-resource"), Title: "付费资源", Currency: "CNY", Amount: 990, Channel: "gateway", Accepted: true, URL: "/posts/resource#resource-bundle"}
	first, e := service.CreateResourcePayment(t.Context(), in)
	if e != nil {
		t.Fatal(e)
	}
	second, e := service.CreateResourcePayment(t.Context(), in)
	if e != nil || first.ID != second.ID {
		t.Fatal("duplicate order", e)
	}
	notify, _ := json.Marshal(payment.Notification{OrderID: first.ID, ProviderOrderID: "pg-" + first.ID, TransactionID: "resource-transaction", Amount: 990, Currency: "CNY", Status: payment.Paid})
	for range 2 {
		if _, e = service.Notify(t.Context(), first.ID, http.Header{"X-Test-Verified": []string{"true"}}, notify); e != nil {
			t.Fatal(e)
		}
	}
	order, e := service.Order(t.Context(), ids[0], first.ID)
	if e != nil || order.Status != "completed" || order.CreditEntry != 0 || order.MemberEntry != 0 || order.ResourceURL != in.URL {
		t.Fatal(order.Status, e)
	}
}

func TestPostgresResourcePointPurchaseSnapshotAndReceiptPersistence(t *testing.T) {
	coin, cfg, ids := currencyPostgres(t)
	store := &resources.Store{Backend: NewResourceBackend(coin.pool)}
	if _, err := coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: ids[0], UserID: ids[0], Delta: 1000, ExpectedVersion: cfg.Version, RequestID: "postgres-resource-funding", Reason: "资源积分回归"}); err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(time.Hour).UTC()
	p, err := store.BeginPointPurchase(t.Context(), resources.PointPurchase{UserID: ids[0], Terms: resources.PurchaseTerms{PostID: 1, Resource: "bundle", Variant: "full", Condition: "price", Kind: "points", Currency: "main", Amount: 100, EndsAt: &end}, CurrencyVersion: cfg.Version, Precision: cfg.Precision, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	in := p.Payment()
	debit := virtualcurrency.SpendInput{UserID: in.UserID, Amount: in.Amount, ExpectedVersion: in.ExpectedVersion, RequestID: in.RequestID, Reason: in.Reason}
	entry, _, err := coin.Spend(t.Context(), debit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinishPointPurchase(t.Context(), p, resources.PointReceipt{ID: entry.ID, UserID: entry.UserID, Amount: entry.PurchaseAmount, RequestID: debit.RequestID, Paid: true, CreatedAt: entry.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	state, err := NewResourceBackend(coin.pool).Read(t.Context())
	if err != nil || state.Purchases[p.Key()].PaymentEntry != entry.ID {
		t.Fatal("purchase snapshot lost", err)
	}
	history, err := coin.PurchaseHistory(t.Context(), ids[0])
	if err != nil || len(history) != 1 || history[0].ID != entry.ID {
		t.Fatal("legacy receipt lookup lost", history, err)
	}
	changed := p
	changed.Terms.Amount = 200
	changed.Terms.Currency = "bonus"
	replayed, err := store.BeginPointPurchase(t.Context(), changed)
	if err != nil || replayed.Terms.Amount != 100 || replayed.Terms.Currency != "main" {
		t.Fatal("repricing rewrote persisted payment", replayed, err)
	}
}
