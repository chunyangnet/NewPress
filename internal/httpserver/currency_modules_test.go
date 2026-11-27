package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/virtualcurrency"
)

func TestCurrencyCatalogHTTPPermissionsAndCreation(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	main, cfg := currencyHTTPStore(t, s, false)
	_, readerCookie, reader := createAndLoginTestUser(t, manager, owner, "catalog-reader", "catalog-reader@example.test", "读者", "subscriber")
	headers := authenticatedAPIHeaders(owner)
	base := "https://example.test/api/v1/admin/currencies"
	valid := `{"key":"gold","name":"金币","symbol":"枚","precision":2,"request_id":"http-create-currency-gold"}`
	for _, path := range []string{base, base + "/gold", "https://example.test/api/v1/social/wallets", "https://example.test/api/v1/social/wallets/gold"} {
		if r := serveContentAPI(handler, nil, "GET", path, "", nil); r.Code != 401 {
			t.Fatal("anonymous read", path, r.Code)
		}
	}
	for _, path := range []string{base, base + "/gold"} {
		if r := serveContentAPI(handler, readerCookie, "GET", path, "", nil); r.Code != 403 {
			t.Fatal("reader admin access", r.Code)
		}
	}
	if r := serveContentAPI(handler, readerCookie, "POST", base, valid, authenticatedAPIHeaders(reader)); r.Code != 403 {
		t.Fatal("reader created currency", r.Code)
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{strings.TrimSuffix(valid, "}") + `,"actor_id":1}`, 400},
		{strings.Replace(valid, `"precision":2`, `"precision":null`, 1), 400},
		{strings.Replace(valid, `"key":"gold"`, `"key":"gold","key":"silver"`, 1), 400},
		{strings.Replace(valid, `"key":"gold"`, `"Key":"gold"`, 1), 400},
		{strings.Replace(valid, `"precision":2,`, "", 1), 400},
		{strings.Replace(valid, `"gold"`, `"../gold"`, 1), 422},
	} {
		if r := serveContentAPI(handler, cookie, "POST", base, tc.body, headers); r.Code != tc.status {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	for i := range 2 {
		r := serveContentAPI(handler, cookie, "POST", base, valid, headers)
		want := 201
		if i > 0 {
			want = 200
		}
		if r.Code != want {
			t.Fatal(r.Code, r.Body.String())
		}
		var result struct {
			CurrencyKey string              `json:"currency_key"`
			Settings    currencySettingsDTO `json:"settings"`
			Replayed    bool                `json:"replayed"`
		}
		decodeAPIResponse(t, r, &result)
		if result.CurrencyKey != "gold" || result.Settings.Enabled || result.Settings.Version != "1" || result.Replayed != (i > 0) {
			t.Fatal(result)
		}
	}
	assertAPIError(t, serveContentAPI(handler, cookie, "POST", base, strings.Replace(valid, "金币", "另一个币", 1), headers), 409, "currency.request_reused")
	r := serveContentAPI(handler, readerCookie, "GET", "https://example.test/api/v1/social/wallets", "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"key":"gold"`) || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, path := range []string{base + "/missing", base + "/missing/wallet?username=owner", "https://example.test/api/v1/social/wallets/missing"} {
		assertAPIError(t, serveContentAPI(handler, cookie, "GET", path, "", nil), 404, "currency.not_found")
	}
	assertAPIError(t, serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/social/wallets/main?currency=gold", "", nil), 400, "request.invalid_query")
	current, err := main.Settings(t.Context())
	if err != nil || current != cfg {
		t.Fatal("currency creation changed main", current, err)
	}
}

func TestCurrencyModuleHTTPAtomicScopeAndVersion(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	main, mainCfg := currencyHTTPStore(t, s, false)
	item, _, err := s.currencyCatalog.Create(t.Context(), virtualcurrency.CreateCurrency{Key: "gold", Name: "金币", Symbol: "枚", Precision: 2, ActorID: owner.UserID, RequestID: "http-module-create-gold"})
	if err != nil {
		t.Fatal(err)
	}
	gold, _ := s.currencyCatalog.Store(t.Context(), item.Key)
	headers := authenticatedAPIHeaders(owner)
	base := "https://example.test/api/v1/admin/currencies/gold/modules/"
	patch := func(module, version string, values any) currencySettingsDTO {
		t.Helper()
		r := serveContentAPI(handler, cookie, "PATCH", base+module, currencyJSON(t, map[string]any{"version": version, "values": values}), headers)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		var result struct {
			CurrencyKey string              `json:"currency_key"`
			Module      string              `json:"module"`
			Settings    currencySettingsDTO `json:"settings"`
		}
		decodeAPIResponse(t, r, &result)
		if result.CurrencyKey != "gold" || result.Module != module {
			t.Fatal(result)
		}
		return result.Settings
	}
	value := patch("check-in", "1", map[string]any{"check_in_enabled": true, "check_in_reward": "0.75", "check_in_interval_hours": 48, "check_in_mode": "calendar_day", "check_in_timezone": "Asia/Shanghai"})
	if value.Version != "2" || value.CheckInReward != "0.75" || value.CheckInIntervalHours != 48 || value.CheckInMode != "calendar_day" || value.CheckInTimezone != "Asia/Shanghai" {
		t.Fatal(value)
	}
	for _, tc := range []struct {
		module, body string
		status       int
		code         string
	}{
		{"check-in", `{"version":"2","values":{"enabled":true}}`, 422, "currency.invalid"},
		{"check-in", `{"version":"2","values":{"check_in_reward":"100.00"}}`, 422, "currency.invalid"},
		{"check-in", `{"version":"1","values":{"check_in_reward":"0.90"}}`, 409, "currency.settings_conflict"},
		{"check-in", `{"version":"2","values":{"check_in_reward":null}}`, 400, "request.invalid_json"},
		{"check-in", `{"version":"2","values":{"check_in_reward":"0.50","check_in_reward":"0.60"}}`, 400, "request.invalid_json"},
		{"check-in", `{"version":"2","values":{"check_in_interval_hours":"24"}}`, 422, "currency.invalid"},
		{"check-in", `{"version":"2","values":{}}`, 422, "currency.invalid"},
		{"registration", `{"version":"2","values":{"registration_since":"2020-01-01"}}`, 422, "currency.invalid"},
		{"identity", `{"version":"2","values":{"precision_locked":false}}`, 422, "currency.invalid"},
		{"missing", `{"version":"2","values":{"enabled":true}}`, 404, "currency.module_not_found"},
	} {
		assertAPIError(t, serveContentAPI(handler, cookie, "PATCH", base+tc.module, tc.body, headers), tc.status, tc.code)
	}
	current, _ := gold.Settings(t.Context())
	if current.Version != 2 || current.RegistrationReward != item.Settings.RegistrationReward || current.AllowTransfers {
		t.Fatal("failed patch mutated unrelated fields", current)
	}
	value = patch("identity", "2", map[string]any{"precision": 3, "name": "收藏金币"})
	current, _ = gold.Settings(t.Context())
	if value.CheckInReward != "0.075" || current.CheckInReward != 75 || current.MinTransfer != item.Settings.MinTransfer || current.MaxTransfer != item.Settings.MaxTransfer || current.Precision != 3 {
		t.Fatal("precision edit rescaled other modules", current)
	}
	if cfg, _ := main.Settings(t.Context()); cfg != mainCfg {
		t.Fatal("module changed main currency", cfg)
	}
	_, err = gold.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: owner.UserID, Delta: 50, ExpectedVersion: current.Version, RequestID: "module-lock-precision-test", Reason: "精度锁验证"})
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, serveContentAPI(handler, cookie, "PATCH", base+"identity", currencyJSON(t, map[string]any{"version": value.Version, "values": map[string]any{"precision": 4}}), headers), 409, "currency.precision_locked")
	changed := currencySettingsView(mainCfg)
	changed.Name = "默认积分"
	r := serveContentAPI(handler, cookie, "PUT", "https://example.test/api/v1/admin/currencies/main", currencyJSON(t, map[string]any{"settings": changed}), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	_ = patch("registration", "3", map[string]any{"registration_reward": "0.100"})
}

func TestCurrencyHTTPScopedOperationsAndHTMLFallback(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	main, mainCfg := currencyHTTPStore(t, s, false)
	item, _, err := s.currencyCatalog.Create(t.Context(), virtualcurrency.CreateCurrency{Key: "gold", Name: "金币", Symbol: "枚", Precision: 2, ActorID: owner.UserID, RequestID: "http-fallback-create-gold"})
	if err != nil {
		t.Fatal(err)
	}
	gold, _ := s.currencyCatalog.Store(t.Context(), item.Key)
	body := fmt.Sprintf(`{"user_id":"%d","delta":"1.25","reason":"金币发放","version":"1","request_id":"http-scoped-adjust-request"}`, owner.UserID)
	headers := authenticatedAPIHeaders(owner)
	for range 2 {
		r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/currencies/gold/adjust", body, headers)
		if r.Code != 201 || !strings.Contains(r.Body.String(), `"currency_key":"gold"`) {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if balance, _ := gold.Balance(t.Context(), owner.UserID); balance != 125 {
		t.Fatal(balance)
	}
	if balance, _ := main.Balance(t.Context(), owner.UserID); balance != 0 {
		t.Fatal("cross-currency adjustment", balance)
	}
	body = strings.Replace(body, `"version":"1"`, `"version":"`+strconv.FormatInt(mainCfg.Version, 10)+`"`, 1)
	if r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/currencies/main/adjust", body, headers); r.Code != 201 {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, path := range []string{"/api/v1/social/wallets/gold", "/api/v1/admin/currencies/gold/wallet?username=owner"} {
		r := serveContentAPI(handler, cookie, "GET", "https://example.test"+path, "", nil)
		var wallet currencyWalletDTO
		decodeAPIResponse(t, r, &wallet)
		if r.Code != 200 || wallet.CurrencyKey != "gold" || wallet.Balance != "1.25" || len(wallet.Entries) != 1 {
			t.Fatal(wallet, r.Body.String())
		}
	}
	r := serveContentAPI(handler, cookie, "GET", "https://example.test/admin/currency/?currency=gold", "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `data-currency-key="gold"`) || strings.Count(r.Body.String(), `data-currency-module="`) != 6 {
		t.Fatal(r.Code, r.Body.String())
	}
	form := url.Values{"action": {"settings"}, "module": {"check-in"}, "version": {"1"}, "csrf_token": {owner.CSRFToken}, "check_in_enabled": {"on"}, "check_in_reward": {"0.25"}, "check_in_interval_hours": {"12"}, "name": {"未保存的其他模块"}}
	r = serveContentAPI(handler, cookie, "POST", "https://example.test/admin/currency/?currency=gold", form.Encode(), map[string]string{"Origin": "https://example.test", "Content-Type": "application/x-www-form-urlencoded"})
	if r.Code != http.StatusSeeOther || !strings.Contains(r.Header().Get("Location"), "currency=gold") {
		t.Fatal(r.Code, r.Body.String())
	}
	cfg, _ := gold.Settings(t.Context())
	if cfg.Name != "金币" || cfg.CheckInReward != 25 || cfg.CheckInIntervalHours != 12 || !cfg.CheckInEnabled {
		t.Fatal("HTML module scope", cfg)
	}
	assertAPIError(t, serveContentAPI(handler, cookie, "GET", "https://example.test/admin/currency/?currency=gold&currency=main", "", nil), 400, "request.invalid_query")
}
