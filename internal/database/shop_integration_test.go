package database

import (
	"errors"
	"fmt"
	"newpress.local/newpress/internal/shop"
	"sync"
	"testing"
)

func TestPostgreSQLShopConcurrentReservationAndReceiptRecovery(t *testing.T) {
	coin, _, ids := currencyPostgres(t)
	s := &shop.Service{Backend: NewShopBackend(coin.pool)}
	c := shop.InitialState().Catalog
	c.Enabled = true
	c.Products = []shop.Product{{Key: "code", Title: "并发库存", Kind: "cdk", Mode: "direct", Currency: "CNY", Price: 100, Stock: 1, Enabled: true, Codes: []string{"ONE-CODE"}}}
	c, err := s.SaveCatalog(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	winners := make(chan shop.Order, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := s.Create(t.Context(), shop.Purchase{QuoteInput: shop.QuoteInput{Product: "code", Quantity: 1}, UserID: ids[1], Version: c.Version, RequestID: fmt.Sprintf("attempt-%d", i), Accepted: true})
			if e == nil {
				winners <- o
			} else if !errors.Is(e, shop.ErrConflict) && !errors.Is(e, shop.ErrStock) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("successful reservations: %d", len(winners))
	}
	order := <-winners
	receipt := shop.PaymentReceipt{OrderID: order.ID, UserID: ids[1], Index: 0, Reference: "pg-receipt", Amount: 100, Currency: "CNY"}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.MarkPaid(t.Context(), receipt); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	reopened := &shop.Service{Backend: NewShopBackend(coin.pool)}
	order, err = reopened.Order(t.Context(), ids[1], order.ID)
	if err != nil || order.Status != "completed" || len(order.Deliveries) != 1 || order.Deliveries[0].Content != "ONE-CODE" {
		t.Fatal(order, err)
	}
	review, err := reopened.CreateReview(t.Context(), ids[1], order.ID, "数据库买家", shop.ReviewInput{Rating: 5, Content: "交付及时。"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.ModerateReview(t.Context(), ids[0], review.ID, shop.ReviewModeration{Version: 1, Reply: "已收到反馈。"}); err != nil {
		t.Fatal(err)
	}
	reopened = &shop.Service{Backend: NewShopBackend(coin.pool)}
	page, err := reopened.ProductReviews(t.Context(), shop.ReviewQuery{Product: "code", Page: 1})
	if err != nil || page.Summary.Count != 1 || page.Reviews[0].Reply != "已收到反馈。" {
		t.Fatal(page, err)
	}
}
