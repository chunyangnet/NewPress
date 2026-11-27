package backup

import (
	"newpress.local/newpress/internal/shop"
	"path/filepath"
	"testing"
)

func TestShopBackupRestoresReservedAndDeliveredCodes(t *testing.T) {
	f := fixtureSite(t, false)
	backend, err := shop.NewFileBackend(filepath.Join(f.cfg.DataDir, "shop.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := &shop.Service{Backend: backend}
	c := shop.InitialState().Catalog
	c.Enabled = true
	c.Products = []shop.Product{{Key: "code", Title: "备份卡密", Kind: "cdk", Mode: "direct", Currency: "CNY", Price: 100, Stock: 2, Enabled: true, Codes: []string{"ISSUED-CODE", "RESERVED-CODE"}}}
	c, err = service.SaveCatalog(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	var orders []shop.Order
	for _, request := range []string{"paid", "pending"} {
		c, _ = service.Catalog(t.Context())
		o, e := service.Create(t.Context(), shop.Purchase{QuoteInput: shop.QuoteInput{Product: "code", Quantity: 1}, UserID: 1, Version: c.Version, RequestID: request, Accepted: true})
		if e != nil {
			t.Fatal(e)
		}
		orders = append(orders, o)
	}
	receipt := shop.PaymentReceipt{OrderID: orders[0].ID, UserID: 1, Index: 0, Reference: "backup-receipt", Amount: 100, Currency: "CNY"}
	if _, err = service.MarkPaid(t.Context(), receipt); err != nil {
		t.Fatal(err)
	}
	review, err := service.CreateReview(t.Context(), 1, orders[0].ID, "买家", shop.ReviewInput{Rating: 4, Content: "交付完整。"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ModerateReview(t.Context(), 1, review.ID, shop.ReviewModeration{Version: 1, Reply: "谢谢反馈。"}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(f.parent, "shop.zip")
	if _, err = Create(t.Context(), f.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(f.parent, "restored-shop"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	backend, err = shop.NewFileBackend(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "shop.json"))
	if err != nil {
		t.Fatal(err)
	}
	service = &shop.Service{Backend: backend}
	reviews, err := service.ProductReviews(t.Context(), shop.ReviewQuery{Product: "code", Page: 1})
	if err != nil || reviews.Summary.Count != 1 || reviews.Reviews[0].Reply != "谢谢反馈。" {
		t.Fatal(reviews, err)
	}
	paid, err := service.MarkPaid(t.Context(), receipt)
	if err != nil || len(paid.Deliveries) != 1 || paid.Deliveries[0].Content != "ISSUED-CODE" {
		t.Fatal(paid, err)
	}
	if _, err = service.Cancel(t.Context(), 1, orders[1].ID); err != nil {
		t.Fatal(err)
	}
	c, err = service.Catalog(t.Context())
	if err != nil || c.Products[0].Stock != 1 || len(c.Products[0].Codes) != 1 || c.Products[0].Codes[0] != "RESERVED-CODE" {
		t.Fatal(c, err)
	}
}
