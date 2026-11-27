package httpserver

import (
	"fmt"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/recognition"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/shop"
	"newpress.local/newpress/internal/virtualcurrency"
)

func TestMainPointsTenthsHTTPTransferRewardShopResourceAndTip(t *testing.T) {
	s, h, accounts, repo, cookie, owner := newPersistentAdminTestServer(t)
	coin, err := virtualcurrency.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := virtualcurrency.DefaultSettings()
	cfg.Enabled, cfg.AllowTransfers, cfg.CheckInEnabled = true, true, true
	cfg.RequireVerifiedEmail = false
	cfg.CheckInReward = 1
	cfg, err = coin.UpdateSettings(t.Context(), 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.SetCurrencyStore(coin)
	_, readerCookie, reader := createAndLoginTestUser(t, accounts, owner, "tenths-reader", "tenths-reader@example.test", "积分买家", "subscriber")
	headers := authenticatedAPIHeaders(owner)
	readerHeaders := authenticatedAPIHeaders(reader)
	adjust := fmt.Sprintf(`{"user_id":"%d","delta":"0.3","reason":"积分精度测试","request_id":"tenths-http-adjust","version":"%d"}`, owner.UserID, cfg.Version)
	for range 2 {
		w := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/currencies/main/adjust", adjust, headers)
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	bad := strings.ReplaceAll(strings.ReplaceAll(adjust, "0.3", "0.01"), "tenths-http-adjust", "tenths-invalid-adjust")
	if w := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/currencies/main/adjust", bad, headers); w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
	transfer := fmt.Sprintf(`{"recipient_id":"%d","amount":"0.1","request_id":"tenths-http-transfer","version":"%d"}`, reader.UserID, cfg.Version)
	for range 2 {
		w := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/wallets/main/transfer", transfer, headers)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/social/wallets/main", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"balance":"0.2"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	checkin := fmt.Sprintf(`{"version":"%d","request_id":"tenths-http-check-in"}`, cfg.Version)
	for range 2 {
		w := serveContentAPI(h, readerCookie, "POST", "https://example.test/api/v1/social/wallets/main/check-in", checkin, readerHeaders)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if b, _ := coin.Balance(t.Context(), reader.UserID); b != 2 {
		t.Fatal("repeated transfer or reward", b)
	}
	backend, err := shop.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	svc := &shop.Service{Backend: backend, Currencies: s.currencyCatalog, Users: accounts}
	sc := shop.InitialState().Catalog
	sc.Enabled = true
	sc.Products = []shop.Product{{Key: "tenths", Title: "0.1 积分商品", Kind: "virtual", Mode: "direct", Enabled: true, Currency: "wallet:main", Precision: 1, Price: 1, Stock: 10, Delivery: "https://example.test/private-download"}}
	sc, err = svc.SaveCatalog(t.Context(), sc)
	if err != nil {
		t.Fatal(err)
	}
	s.SetShopService(svc)
	body := fmt.Sprintf(`{"product":"tenths","quantity":1,"version":"%d","request_id":"tenths-http-shop-order","accepted":true}`, sc.Version)
	w := serveContentAPI(h, readerCookie, "POST", "https://example.test/api/v1/shop/orders", body, readerHeaders)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		Order shop.Order `json:"order"`
	}
	decodeAPIResponse(t, w, &created)
	for range 2 {
		w = serveContentAPI(h, readerCookie, "POST", "https://example.test/api/v1/shop/orders/"+created.Order.ID+"/pay", `{"index":0,"channel":"wallet","accepted":true}`, readerHeaders)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if b, _ := coin.Balance(t.Context(), reader.UserID); b != 1 {
		t.Fatal("shop debit", b)
	}
	rcfg, err := resources.Normalize(resources.Config{Items: []resources.Resource{{ID: "book", Title: "0.1 积分资源", Variants: []resources.Variant{{ID: "full", Name: "全文", Kind: "content", Body: "<p>解锁全文</p>", Groups: []resources.Group{{Name: "积分", Conditions: []resources.Condition{{ID: "price", Kind: "points", Currency: "main", Amount: 1}}}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	p := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, Title: "0.1 积分文章", Slug: "tenths-resource", Status: content.StatusPublish, ContentSource: "<p>公开内容</p>", Resources: &rcfg})
	r := resources.FromMeta(p.Meta).Items[0]
	endpoint := fmt.Sprintf("https://example.test/api/v1/article-resources/%d/book", p.ID)
	if w = serveContentAPI(h, readerCookie, "GET", endpoint, "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "0.1 积分") {
		t.Fatal(w.Code, w.Body.String())
	}
	action := resourceActionInput{Action: "points", Variant: "full", Condition: "price", Group: 0, Revision: resources.Hash(r), Accepted: true}
	for range 2 {
		w = serveContentAPI(h, readerCookie, "POST", endpoint, currencyJSON(t, action), readerHeaders)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if b, _ := coin.Balance(t.Context(), reader.UserID); b != 0 {
		t.Fatal("resource debit", b)
	}
	if _, err = svc.RequestRefund(t.Context(), reader.UserID, created.Order.ID, "退款"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ReviewRefund(t.Context(), created.Order.ID, true, "通过"); err != nil {
		t.Fatal(err)
	}
	if b, _ := coin.Balance(t.Context(), reader.UserID); b != 1 {
		t.Fatal("0.1 refund", b)
	}
	tipBackend, err := recognition.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	tips := &recognition.Service{Backend: tipBackend, Currencies: s.currencyCatalog, Users: accounts, Content: repo}
	ts := recognition.InitialState().Settings
	ts.TipsEnabled = true
	if err = tips.SaveSettings(t.Context(), ts); err != nil {
		t.Fatal(err)
	}
	s.SetRecognitionService(tips)
	tip := recognition.TipInput{PostID: p.ID, RequestID: "tenths-http-article-tip", Currency: "main", Amount: "0.1", SettingsVersion: 2, CurrencyVersion: cfg.Version}
	for range 2 {
		w = serveContentAPI(h, readerCookie, "POST", fmt.Sprintf("https://example.test/api/v1/social/tips/%d", p.ID), currencyJSON(t, tip), readerHeaders)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if b, _ := coin.Balance(t.Context(), reader.UserID); b != 0 {
		t.Fatal("tip debit", b)
	}
}
