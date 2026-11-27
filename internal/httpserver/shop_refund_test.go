package httpserver

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/shop"
	"newpress.local/newpress/internal/virtualcurrency"
)

type shopRefundHTTPFixture struct {
	server  *Server
	handler http.Handler
	cookie  *http.Cookie
	owner   auth.Principal
	service *shop.Service
	ledger  *virtualcurrency.FileStore
	orders  map[string]string
}

func newShopRefundHTTPFixture(t *testing.T) *shopRefundHTTPFixture {
	t.Helper()
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	dir := t.TempDir()
	ledger, err := virtualcurrency.NewFileStore(filepath.Join(dir, "currency.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ledger.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled, cfg.RequireVerifiedEmail = true, false
	cfg, err = ledger.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.Credit(t.Context(), virtualcurrency.CreditInput{UserID: owner.UserID, Amount: 500, ExpectedVersion: cfg.Version, RequestID: "shop-refund-http-funds", Source: "isolated refund fixture"}); err != nil {
		t.Fatal(err)
	}
	backend, err := shop.NewFileBackend(filepath.Join(dir, "shop.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := &shop.Service{Backend: backend, Currencies: virtualcurrency.NewMemoryCatalog(ledger)}
	catalog := shop.InitialState().Catalog
	catalog.Enabled = true
	catalog.Products = []shop.Product{
		{Key: "wallet-review", Title: "钱包退款审核测试", Kind: "physical", Mode: "direct", Enabled: true, Currency: "wallet:main", Precision: cfg.Precision, Price: 80, Stock: 3},
		{Key: "wallet-retry", Title: "钱包退款重试测试", Kind: "physical", Mode: "direct", Enabled: true, Currency: "wallet:main", Precision: cfg.Precision, Price: 80, Stock: 3},
		{Key: "external-refund", Title: "渠道退款登记测试", Kind: "physical", Mode: "direct", Enabled: true, Currency: "CNY", Price: 8000, Stock: 3},
	}
	if _, err = service.SaveCatalog(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	orders := map[string]string{}
	for _, product := range catalog.Products {
		current, err := service.Catalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		order, err := service.Create(t.Context(), shop.Purchase{QuoteInput: shop.QuoteInput{Product: product.Key, Quantity: 1}, UserID: owner.UserID, Version: current.Version, RequestID: "refund-http-" + product.Key, Accepted: true, Address: shop.Address{Name: "测试收货人", Phone: "123456789", Detail: "测试收货地址"}})
		if err != nil {
			t.Fatal(err)
		}
		if product.Currency == "CNY" {
			_, err = service.MarkPaid(t.Context(), shop.PaymentReceipt{OrderID: order.ID, UserID: owner.UserID, Index: 0, Reference: "fixture-payment-" + order.ID, Amount: order.Total, Currency: "CNY"})
		} else {
			_, err = service.PayWallet(t.Context(), owner.UserID, order.ID, 0)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.RequestRefund(t.Context(), owner.UserID, order.ID, "测试退款申请"); err != nil {
			t.Fatal(err)
		}
		if product.Currency == "CNY" {
			if _, err = service.ReviewRefund(t.Context(), order.ID, true, "批准渠道退款"); err != nil {
				t.Fatal(err)
			}
		}
		orders[product.Key] = order.ID
	}
	cfg, err = ledger.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	if _, err = ledger.UpdateSettings(t.Context(), cfg.Version, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ReviewRefund(t.Context(), orders["wallet-retry"], true, "批准钱包退款"); !errors.Is(err, virtualcurrency.ErrDisabled) {
		t.Fatalf("fixture did not retain a failed approved wallet refund: %v", err)
	}
	now := time.Now()
	if _, err = service.SaveCoupons(t.Context(), 0, []shop.Coupon{{Code: "DELETE-LAST", Title: "最后一张未领取优惠券", Kind: "fixed", Value: 100, Currency: "CNY", PerUserLimit: 1, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	s.SetShopService(service)
	return &shopRefundHTTPFixture{server: s, handler: handler, cookie: cookie, owner: owner, service: service, ledger: ledger, orders: orders}
}

func (f *shopRefundHTTPFixture) enableWallet(t *testing.T) {
	t.Helper()
	cfg, err := f.ledger.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	if _, err = f.ledger.UpdateSettings(t.Context(), cfg.Version, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestShopHTTPWalletRefundRequiresCreditAndExternalRefundKeepsReceipt(t *testing.T) {
	f := newShopRefundHTTPFixture(t)
	url := func(key, action string) string {
		return "https://example.test/api/v1/admin/shop/orders/" + f.orders[key] + "/" + action
	}
	w := serveContentAPI(f.handler, nil, "POST", url("wallet-retry", "refund"), `{}`, authenticatedAPIHeaders(f.owner))
	if w.Code != 401 {
		t.Fatalf("anonymous wallet refund accepted: %d/%s", w.Code, w.Body.String())
	}
	w = serveContentAPI(f.handler, f.cookie, "POST", url("wallet-retry", "refund"), `{}`, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"})
	if w.Code != 403 {
		t.Fatalf("wallet refund without CSRF accepted: %d/%s", w.Code, w.Body.String())
	}
	w = serveContentAPI(f.handler, f.cookie, "POST", url("wallet-review", "refund"), `{}`, authenticatedAPIHeaders(f.owner))
	if w.Code != 409 {
		t.Fatalf("wallet refund bypassed approval: %d/%s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{}`, `{"reference":"manual-external-reference","note":"fake external receipt"}`} {
		w = serveContentAPI(f.handler, f.cookie, "POST", url("wallet-retry", "refund"), body, authenticatedAPIHeaders(f.owner))
		if w.Code != 503 {
			t.Fatalf("disabled wallet was marked refunded: %d/%s", w.Code, w.Body.String())
		}
	}
	if o, err := f.service.Order(t.Context(), 0, f.orders["wallet-retry"]); err != nil || o.Status != "refund_required" {
		t.Fatalf("failed wallet refund changed status: %+v/%v", o, err)
	}
	w = serveContentAPI(f.handler, f.cookie, "POST", url("external-refund", "refund"), `{}`, authenticatedAPIHeaders(f.owner))
	if w.Code != 422 {
		t.Fatalf("external refund accepted without a receipt: %d/%s", w.Code, w.Body.String())
	}
	f.enableWallet(t)
	for range 2 {
		w = serveContentAPI(f.handler, f.cookie, "POST", url("wallet-retry", "refund"), `{}`, authenticatedAPIHeaders(f.owner))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"refunded"`) || !strings.Contains(w.Body.String(), "wallet-refund-"+f.orders["wallet-retry"]) {
			t.Fatalf("wallet retry did not return a verified refund: %d/%s", w.Code, w.Body.String())
		}
	}
	w = serveContentAPI(f.handler, f.cookie, "POST", url("wallet-review", "refund-review"), `{"approved":true,"note":"approved"}`, authenticatedAPIHeaders(f.owner))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"refunded"`) {
		t.Fatalf("approved wallet refund did not credit the wallet: %d/%s", w.Code, w.Body.String())
	}
	if balance, err := f.ledger.Balance(t.Context(), f.owner.UserID); err != nil || balance != 500 {
		t.Fatalf("wallet refund amount was incorrect: %d/%v", balance, err)
	}
	w = serveContentAPI(f.handler, f.cookie, "POST", url("external-refund", "refund"), `{"reference":"verified-provider-refund","note":"amount checked"}`, authenticatedAPIHeaders(f.owner))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"refund_reference":"verified-provider-refund"`) {
		t.Fatalf("external receipt flow changed: %d/%s", w.Code, w.Body.String())
	}
	if err := f.service.ValidateWallet(t.Context()); err != nil {
		t.Fatal(err)
	}
	w = serveContentAPI(f.handler, f.cookie, "PUT", "https://example.test/api/v1/admin/shop/coupons", `{"version":1,"coupons":[]}`, authenticatedAPIHeaders(f.owner))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"coupons":[]`) {
		t.Fatalf("empty coupon response was not an array: %d/%s", w.Code, w.Body.String())
	}
}
