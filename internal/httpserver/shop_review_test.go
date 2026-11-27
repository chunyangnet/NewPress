package httpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"newpress.local/newpress/internal/shop"
)

func TestShopHTTPReviewsAuthorizationAndModeration(t *testing.T) {
	s, h, manager, _, adminCookie, admin := newPersistentAdminTestServer(t)
	b, err := shop.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	service := &shop.Service{Backend: b}
	s.SetShopService(service)
	c := shop.InitialState().Catalog
	c.Enabled = true
	c.Products = []shop.Product{{Key: "reviewed", Title: "评价商品", Kind: "virtual", Mode: "direct", Enabled: true, Currency: "CNY", Price: 1000, Stock: 5, Delivery: "secret delivery"}}
	c, err = service.SaveCatalog(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	_, cookie, user := createAndLoginTestUser(t, manager, admin, "review-buyer", "review@example.test", "评价买家", "subscriber")
	o, err := service.Create(t.Context(), shop.Purchase{QuoteInput: shop.QuoteInput{Product: "reviewed", Quantity: 1}, UserID: user.UserID, Version: c.Version, RequestID: "review-http-purchase", Accepted: true})
	if err != nil {
		t.Fatal(err)
	}
	path := "https://example.test/api/v1/shop/orders/" + o.ID + "/review"
	for _, p := range []string{path, "https://example.test/api/v1/admin/shop/reviews"} {
		w := serveContentAPI(h, nil, "GET", p, "", nil)
		if w.Code != 401 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := serveContentAPI(h, adminCookie, "GET", path, "", nil)
	if w.Code != 404 {
		t.Fatal("owner", w.Code)
	}
	w = serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/shop/reviews", "", nil)
	if w.Code != 403 {
		t.Fatal("admin", w.Code)
	}
	body := `{"rating":4,"content":"交付及时，使用说明清楚。","anonymous":true}`
	w = serveContentAPI(h, cookie, "POST", path, body, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"})
	if w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	w = serveContentAPI(h, cookie, "POST", path, body, authenticatedAPIHeaders(user))
	if w.Code != 409 {
		t.Fatal("unpaid", w.Code, w.Body.String())
	}
	_, err = service.MarkPaid(t.Context(), shop.PaymentReceipt{OrderID: o.ID, UserID: user.UserID, Index: 0, Reference: "review-payment", Amount: o.Total, Currency: o.Currency})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"rating":5,"content":"评价","user_id":"1"}`, `{"rating":5,"content":"评价","anonymous":null}`, `{"Rating":5,"content":"评价"}`, `{"rating":5,"rating":1,"content":"评价"}`} {
		w = serveContentAPI(h, cookie, "POST", path, bad, authenticatedAPIHeaders(user))
		if w.Code < 400 {
			t.Fatal("malformed", bad, w.Body.String())
		}
	}
	w = serveContentAPI(h, cookie, "POST", path, body, authenticatedAPIHeaders(user))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Review shop.PublicReview `json:"review"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	w = serveContentAPI(h, cookie, "POST", path, body, authenticatedAPIHeaders(user))
	if w.Code != 409 {
		t.Fatal("duplicate", w.Code)
	}
	public := "https://example.test/api/v1/shop/products/reviewed/reviews"
	w = serveContentAPI(h, nil, "GET", public, "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "匿名买家") || strings.Contains(w.Body.String(), o.ID) || strings.Contains(w.Body.String(), "user_id") || strings.Contains(w.Body.String(), "secret delivery") {
		t.Fatal("privacy", w.Code, w.Body.String())
	}
	for _, query := range []string{"?page=0", "?page=-1", "?page=abc", "?rating=6", "?rating=1&rating=2", "?sort=unknown", "?hidden=true"} {
		w = serveContentAPI(h, nil, "GET", public+query, "", nil)
		if w.Code < 400 {
			t.Fatal("query", query, w.Code)
		}
	}
	moderate := "https://example.test/api/v1/admin/shop/reviews/" + result.Review.ID
	update := `{"version":"1","reply":"感谢反馈。","hidden":true,"reason":"包含个人信息"}`
	w = serveContentAPI(h, cookie, "PUT", moderate, update, authenticatedAPIHeaders(user))
	if w.Code != 403 {
		t.Fatal("moderation permission", w.Code)
	}
	w = serveContentAPI(h, adminCookie, "PUT", moderate, update, authenticatedAPIHeaders(admin))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = serveContentAPI(h, nil, "GET", public, "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"count":0`) || strings.Contains(w.Body.String(), result.Review.Content) {
		t.Fatal("hidden", w.Body.String())
	}
	w = serveContentAPI(h, cookie, "GET", path, "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "感谢反馈") || !strings.Contains(w.Body.String(), "包含个人信息") {
		t.Fatal("owner hidden", w.Body.String())
	}
	w = serveContentAPI(h, adminCookie, "PUT", moderate, update, authenticatedAPIHeaders(admin))
	if w.Code != 409 {
		t.Fatal("stale", w.Code)
	}
	w = serveContentAPI(h, adminCookie, "GET", "https://example.test/api/v1/admin/shop/reviews?status=hidden&rating=4", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), o.ID) {
		t.Fatal("admin list", w.Body.String())
	}
}
