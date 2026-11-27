package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/shop"
)

func TestShopHTTPAuthorizationVerifiedPaymentsAndPrivateDelivery(t *testing.T) {
	s, h, manager, _, adminCookie, admin := newPersistentAdminTestServer(t)
	payments, _, _ := commerceHTTPStore(t, s)
	b, e := shop.NewFileBackend("")
	if e != nil {
		t.Fatal(e)
	}
	service := &shop.Service{Backend: b}
	c := shop.InitialState().Catalog
	c.Enabled = true
	c.Products = []shop.Product{{Key: "code", Title: "测试卡密", Kind: "cdk", Mode: "direct", Enabled: true, Currency: "CNY", Price: 1200, Stock: 1, Codes: []string{"PRIVATE-CDK-123"}}}
	c, e = service.SaveCatalog(t.Context(), c)
	if e != nil {
		t.Fatal(e)
	}
	s.SetShopService(service)
	payments.SettleShop = func(ctx context.Context, p commerce.ShopSettlement) error {
		_, e := service.MarkPaid(ctx, shop.PaymentReceipt{OrderID: p.OrderID, UserID: p.UserID, Index: p.Installment - 1, Reference: p.PaymentID, Amount: p.Amount, Currency: p.Currency})
		return e
	}
	_, cookie, user := createAndLoginTestUser(t, manager, admin, "shop-reader", "shop-reader@example.test", "商城买家", "subscriber")
	for _, path := range []string{"/api/v1/shop/orders", "/api/v1/admin/shop", "/api/v1/admin/shop/orders"} {
		w := serveContentAPI(h, nil, "GET", "https://example.test"+path, "", nil)
		if w.Code != 401 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	w := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/shop", "", nil)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = serveContentAPI(h, nil, "GET", "https://example.test/api/v1/shop/catalog", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE-CDK") {
		t.Fatal(w.Code, w.Body.String())
	}
	base := fmt.Sprintf(`{"product":"code","quantity":1,"version":"%d","request_id":"shop-http-order","accepted":true}`, c.Version)
	for _, bad := range []string{strings.Replace(base, `"accepted":true`, `"accepted":null`, 1), strings.Replace(base, `"quantity":1`, `"quantity":1,"quantity":2`, 1), strings.Replace(base, `"quantity":1`, `"Quantity":1`, 1), strings.TrimSuffix(base, "}") + `,"price":"1"}`} {
		w = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/shop/orders", bad, authenticatedAPIHeaders(user))
		if w.Code < 400 {
			t.Fatal("accepted malformed", w.Body.String())
		}
	}
	w = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/shop/orders", base, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"})
	if w.Code != 403 {
		t.Fatal("CSRF", w.Code)
	}
	w = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/shop/orders", base, authenticatedAPIHeaders(user))
	if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE-CDK") {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		Order shop.Order `json:"order"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &created); e != nil {
		t.Fatal(e)
	}
	id := created.Order.ID
	w = serveContentAPI(h, adminCookie, "GET", "https://example.test/api/v1/shop/orders/"+id, "", nil)
	if w.Code != 404 {
		t.Fatal("ownership", w.Code)
	}
	w = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/shop/orders/"+id+"/pay", `{"index":0,"channel":"epay","accepted":true}`, authenticatedAPIHeaders(user))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var paid struct {
		Payment struct {
			ID string `json:"id"`
		} `json:"payment"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &paid)
	for _, body := range []string{epayHTTPNotification(paid.Payment.ID, "0.01", "fixture-merchant-secret"), epayHTTPNotification(paid.Payment.ID, "12.00", "invalid-secret")} {
		w = serveContentAPI(h, nil, "POST", "https://example.test/api/v1/payments/notify/"+paid.Payment.ID, body, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		if w.Code == 200 {
			t.Fatal("bad signature or price accepted")
		}
	}
	for i := 0; i < 2; i++ {
		w = serveContentAPI(h, nil, "POST", "https://example.test/api/v1/payments/notify/"+paid.Payment.ID, epayHTTPNotification(paid.Payment.ID, "12.00", "fixture-merchant-secret"), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/shop/orders/"+id, "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "PRIVATE-CDK-123") || !strings.Contains(w.Body.String(), `"status":"completed"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/social/billing", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"kind":"shop"`) {
		t.Fatal("billing compatibility", w.Code, w.Body.String())
	}
	for _, path := range []string{"/shop/", "/shop/orders/", "/shop/product/example/", "/admin/shop/"} {
		w = serveContentAPI(h, adminCookie, "GET", "https://example.test"+path, "", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "data-shop") {
			t.Fatal(path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("private storefront cache")
		}
	}
}
