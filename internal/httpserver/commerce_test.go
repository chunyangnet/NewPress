package httpserver

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"

	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/payment"
	"newpress.local/newpress/internal/virtualcurrency"
)

func commerceHTTPStore(t *testing.T, s *Server) (*commerce.Service, commerce.Settings, *virtualcurrency.FileStore) {
	t.Helper()
	members, _, coin := progressionHTTPStore(t, s)
	backend, err := commerce.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	vault, err := commerce.NewVault(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Seal(payment.Config{Provider: "epay", Endpoint: "https://payments.example.test", MerchantID: "merchant1", Secret: "fixture-merchant-secret"})
	if err != nil {
		t.Fatal(err)
	}
	service := &commerce.Service{Backend: backend, Vault: vault, Users: s.auth, Members: members, Currencies: s.currencyCatalog, PublicURL: "https://example.test"}
	cfg := commerce.Defaults()
	cfg.Enabled = true
	cfg.RequireVerifiedEmail = false
	cfg.Channels = []commerce.Channel{{Key: "epay", Name: "测试渠道", Provider: "epay", Currency: "CNY", Enabled: true, SealedConfig: sealed}}
	cfg.Products = []commerce.Product{{Key: "coins", Title: "钱包充值", Kind: "recharge", Currency: "CNY", Price: 1000, TargetCurrency: virtualcurrency.DefaultCurrencyKey, Credit: 500, Enabled: true}}
	cfg, err = service.UpdateSettings(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.SetCommerceService(service)
	return service, cfg, coin
}
func epayHTTPNotification(id, amount, secret string) string {
	return epayHTTPNotificationTrade(id, amount, secret, "trade1")
}
func epayHTTPNotificationTrade(id, amount, secret, transaction string) string {
	v := url.Values{"pid": {"merchant1"}, "out_trade_no": {id}, "trade_no": {transaction}, "money": {amount}, "trade_status": {"TRADE_SUCCESS"}, "sign_type": {"MD5"}}
	keys := []string{}
	for key := range v {
		if key != "sign_type" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := []string{}
	for _, key := range keys {
		parts = append(parts, key+"="+v.Get(key))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + secret))
	v.Set("sign", hex.EncodeToString(sum[:]))
	return v.Encode()
}
func TestCommerceHTTPPrivacyStrictRequestsAndVerifiedSettlement(t *testing.T) {
	s, h, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	service, cfg, coin := commerceHTTPStore(t, s)
	_, readerCookie, reader := createAndLoginTestUser(t, manager, owner, "payment-reader", "payment-reader@example.test", "支付读者", "subscriber")
	for _, path := range []string{"/api/v1/social/billing", "/api/v1/admin/payments", "/api/v1/admin/payments/orders"} {
		r := serveContentAPI(h, nil, "GET", "https://example.test"+path, "", nil)
		if r.Code != 401 {
			t.Fatal(path, r.Code, r.Body.String())
		}
	}
	for _, path := range []string{"/admin/payments/", "/api/v1/admin/payments", "/api/v1/admin/payments/orders"} {
		r := serveContentAPI(h, readerCookie, "GET", "https://example.test"+path, "", nil)
		if r.Code != 403 {
			t.Fatal(path, r.Code, r.Body.String())
		}
	}
	base := fmt.Sprintf(`{"version":"%d","product":"coins","channel":"epay","request_id":"http-payment-one","accepted":true}`, cfg.Version)
	for _, body := range []string{strings.Replace(base, "version", "Version", 1), strings.Replace(base, `"accepted":true`, `"accepted":null`, 1), strings.Replace(base, `"accepted":true`, `"accepted":true,"accepted":true`, 1), strings.Replace(base, `"version":"2",`, "", 1), strings.Replace(base, `"version":"2"`, `"version":"02"`, 1), strings.TrimSuffix(base, "}") + `,"amount":"0.01"}`} {
		r := serveContentAPI(h, readerCookie, "POST", "https://example.test/api/v1/social/billing/orders", body, authenticatedAPIHeaders(reader))
		if r.Code != 400 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	for _, origin := range []string{"", "https://evil.example"} {
		headers := authenticatedAPIHeaders(reader)
		headers["Origin"] = origin
		r := serveContentAPI(h, readerCookie, "POST", "https://example.test/api/v1/social/billing/orders", base, headers)
		if r.Code != 403 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	r := serveContentAPI(h, readerCookie, "POST", "https://example.test/api/v1/social/billing/orders", base, authenticatedAPIHeaders(reader))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var order struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(r.Body.Bytes(), &order) != nil || order.ID == "" {
		t.Fatal(r.Body.String())
	}
	for _, secret := range []string{"fixture-merchant-secret", "wrong-secret"} {
		amount := "10.00"
		if secret == "fixture-merchant-secret" {
			amount = "0.01"
		}
		body := epayHTTPNotification(order.ID, amount, secret)
		r = serveContentAPI(h, nil, "POST", "https://example.test/api/v1/payments/notify/"+order.ID, body, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		if r.Code == 200 {
			t.Fatal("invalid callback accepted")
		}
	}
	r = serveContentAPI(h, ownerCookie, "GET", "https://example.test/api/v1/social/billing/orders/"+order.ID, "", nil)
	if r.Code != 404 {
		t.Fatal("other user's order leaked", r.Code)
	}
	r = serveContentAPI(h, ownerCookie, "GET", "https://example.test/api/v1/social/billing/orders/"+order.ID+"/qr", "", nil)
	if r.Code != 404 {
		t.Fatal("other user's QR leaked", r.Code)
	}
	// Rotation and channel removal must not strand a previously created order.
	cfg.Channels = []commerce.Channel{}
	cfg, err := service.UpdateSettings(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r = serveContentAPI(h, nil, "POST", "https://example.test/api/v1/payments/webhook/epay", epayHTTPNotification(order.ID, "10.00", "fixture-merchant-secret"), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		if r.Code != 200 || r.Body.String() != "success" {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	balance, err := coin.Balance(t.Context(), reader.UserID)
	if err != nil || balance != 500 {
		t.Fatal("duplicate or missing credit", balance, err)
	}
	for _, authCase := range []struct {
		name string
		want int
	}{{"anonymous", 401}, {"reader", 403}, {"owner", 200}} {
		cookie := ownerCookie
		if authCase.name == "anonymous" {
			cookie = nil
		}
		if authCase.name == "reader" {
			cookie = readerCookie
		}
		r = serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/payments/orders/"+order.ID, "", nil)
		if r.Code != authCase.want {
			t.Fatal("exact admin order lookup", authCase.name, r.Code, r.Body.String())
		}
		if authCase.want == 200 {
			var response struct {
				Orders []map[string]any `json:"orders"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil || len(response.Orders) != 1 || response.Orders[0]["id"] != order.ID || response.Orders[0]["transaction_id"] != "trade1" || response.Orders[0]["credit_entry"] == "0" {
				t.Fatal("admin order receipts missing", r.Body.String(), err)
			}
			if strings.Contains(r.Body.String(), "fixture-merchant-secret") || strings.Contains(r.Body.String(), "sealed_config") || r.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("admin order privacy", r.Body.String())
			}
		}
	}
	r = serveContentAPI(h, readerCookie, "GET", "https://example.test/api/v1/social/billing", "", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), "fixture-merchant-secret") || strings.Contains(r.Body.String(), "sealed_config") || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("billing privacy", r.Code, r.Body.String())
	}
	r = serveContentAPI(h, readerCookie, "GET", "https://example.test/api/v1/social/billing?user_id=1", "", nil)
	if r.Code != 400 {
		t.Fatal("ignored query", r.Code)
	}
	_ = cfg
}

func TestCommerceHTTPSecretRetentionAndPublicCheckoutPage(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	service, _, _ := commerceHTTPStore(t, s)
	r := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/payments", "", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), "fixture-merchant-secret") {
		t.Fatal("merchant secret disclosed", r.Code, r.Body.String())
	}
	var payload struct {
		Settings paymentSettingsDTO `json:"settings"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	payload.Settings.Channels[0].Name = "名称修改"
	r = serveContentAPI(h, cookie, "PUT", "https://example.test/api/v1/admin/payments", currencyJSON(t, payload.Settings), authenticatedAPIHeaders(owner))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	cfg, err := service.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	opened, err := service.Vault.Open(cfg.Channels[0].SealedConfig)
	if err != nil || opened.Secret != "fixture-merchant-secret" {
		t.Fatal("blank secret did not preserve credential", err)
	}
	r = serveContentAPI(h, cookie, "GET", "https://example.test/billing/", "", nil)
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" || !strings.Contains(r.Header().Get("X-Robots-Tag"), "noindex") || !strings.Contains(r.Header().Get("Content-Security-Policy"), "form-action 'self' https:") {
		t.Fatal("checkout page boundary", r.Code, r.Header())
	}
}
