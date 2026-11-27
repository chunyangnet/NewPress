package httpserver

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/virtualcurrency"
)

func currencyHTTPStore(t *testing.T, s *Server, rewards bool) (*virtualcurrency.FileStore, virtualcurrency.Settings) {
	t.Helper()
	store, err := virtualcurrency.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	// These existing protocol cases exercise explicit precision-2 minor-unit rewards.
	cfg := virtualcurrency.DefaultSettings()
	cfg.Precision = 0
	cfg.Enabled, cfg.AllowTransfers, cfg.Precision = true, true, 2
	cfg.CheckInEnabled, cfg.RegistrationEnabled, cfg.PublicationEnabled = rewards, rewards, rewards
	cfg.MaxBalance = math.MaxInt64
	cfg, err = store.UpdateSettings(t.Context(), 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.SetCurrencyStore(store)
	return store, cfg
}

func currencyJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCurrencyHTTPProtocolsAndPermissions(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	store, cfg := currencyHTTPStore(t, s, false)
	_, readerCookie, reader := createAndLoginTestUser(t, manager, owner, "currency-reader", "currency-reader@example.test", "读者", "subscriber")
	for _, path := range []string{"/api/v1/social/wallets/main", "/api/v1/social/wallets/main/recipient?username=owner", "/api/v1/admin/currencies/main", "/api/v1/admin/currencies/main/wallet?username=owner"} {
		r := serveContentAPI(handler, nil, "GET", "https://example.test"+path, "", nil)
		if r.Code != 401 {
			t.Fatalf("anonymous %s: %d %s", path, r.Code, r.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/admin/currencies/main", "/api/v1/admin/currencies/main/wallet?username=owner", "/admin/currency/"} {
		r := serveContentAPI(handler, readerCookie, "GET", "https://example.test"+path, "", nil)
		if r.Code != 403 {
			t.Fatalf("reader %s: %d %s", path, r.Code, r.Body.String())
		}
	}
	base := "https://example.test/api/v1/admin/currencies/main/adjust"
	valid := fmt.Sprintf(`{"user_id":"%d","delta":"1.00","reason":"测试发放","request_id":"currency-http-protocol","version":"%d"}`, owner.UserID, cfg.Version)
	for _, tc := range []struct {
		name, body, origin, csrf, ctype string
		status                          int
	}{
		{"cross origin", valid, "https://attacker.test", owner.CSRFToken, "application/json", 403},
		{"missing origin", valid, "", owner.CSRFToken, "application/json", 403},
		{"missing csrf", valid, "https://example.test", "", "application/json", 403},
		{"wrong csrf", valid, "https://example.test", "wrong", "application/json", 403},
		{"wrong content type", valid, "https://example.test", owner.CSRFToken, "text/plain", 415},
		{"duplicate field", strings.Replace(valid, `"delta":"1.00"`, `"delta":"1.00","delta":"2.00"`, 1), "https://example.test", owner.CSRFToken, "application/json", 400},
		{"case alias", strings.Replace(valid, `"delta"`, `"Delta"`, 1), "https://example.test", owner.CSRFToken, "application/json", 400},
		{"null", strings.Replace(valid, `"reason":"测试发放"`, `"reason":null`, 1), "https://example.test", owner.CSRFToken, "application/json", 400},
		{"number instead of money string", strings.Replace(valid, `"delta":"1.00"`, `"delta":1`, 1), "https://example.test", owner.CSRFToken, "application/json", 400},
		{"actor injection", strings.TrimSuffix(valid, "}") + `,"actor_id":"2"}`, "https://example.test", owner.CSRFToken, "application/json", 400},
		{"invalid utf8", strings.Replace(valid, "测试发放", string([]byte{0xff}), 1), "https://example.test", owner.CSRFToken, "application/json", 400},
		{"oversize", strings.Replace(valid, "测试发放", strings.Repeat("a", 9000), 1), "https://example.test", owner.CSRFToken, "application/json", 413},
		{"exponent money", strings.Replace(valid, `"1.00"`, `"1e2"`, 1), "https://example.test", owner.CSRFToken, "application/json", 422},
		{"zero money", strings.Replace(valid, `"1.00"`, `"0"`, 1), "https://example.test", owner.CSRFToken, "application/json", 422},
		{"over precision", strings.Replace(valid, `"1.00"`, `"1.001"`, 1), "https://example.test", owner.CSRFToken, "application/json", 422},
		{"overflow", strings.Replace(valid, `"1.00"`, `"92233720368547758.08"`, 1), "https://example.test", owner.CSRFToken, "application/json", 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := serveContentAPI(handler, cookie, "POST", base, tc.body, map[string]string{"Origin": tc.origin, "X-CSRF-Token": tc.csrf, "Content-Type": tc.ctype})
			if r.Code != tc.status {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
	for _, header := range []string{"X-CSRF-Token", "Origin", "Content-Type"} {
		r := httptest.NewRequest("POST", base, strings.NewReader(valid))
		for key, value := range authenticatedAPIHeaders(owner) {
			r.Header.Set(key, value)
		}
		r.Header.Add(header, r.Header.Get(header))
		r.AddCookie(cookie)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != 403 && out.Code != 415 {
			t.Fatalf("duplicate %s accepted: %d %s", header, out.Code, out.Body.String())
		}
	}
	r := serveContentAPI(handler, readerCookie, "POST", base, valid, authenticatedAPIHeaders(reader))
	if r.Code != 403 {
		t.Fatal("subscriber adjustment accepted", r.Code)
	}
	for _, target := range []string{"/api/v1/social/wallets/main?user_id=1", "/api/v1/social/wallets/main?before=1&before=2", "/api/v1/admin/currencies/main?x=1"} {
		assertAPIError(t, serveContentAPI(handler, cookie, "GET", "https://example.test"+target, "", nil), 400, "request.invalid_query")
	}
	for _, method := range []string{"PUT", "POST"} {
		target := "https://example.test/api/v1/admin/currencies/main?x=1"
		if method == "POST" {
			target = base + "?x=1"
		}
		assertAPIError(t, serveContentAPI(handler, cookie, method, target, valid, authenticatedAPIHeaders(owner)), 400, "request.invalid_query")
	}
	if balance, err := store.Balance(t.Context(), owner.UserID); err != nil || balance != 0 {
		t.Fatal("rejected requests changed balance", balance, err)
	}
	r = serveContentAPI(handler, readerCookie, "GET", "https://example.test/api/v1/social/wallets/main", "", nil)
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" || !strings.Contains(r.Header().Get("Vary"), "Cookie") {
		t.Fatal("wallet cache boundary", r.Code, r.Header())
	}
}

func TestCurrencyAdminPopulatedLedgerRendersInSiteTimezone(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	store, cfg := currencyHTTPStore(t, s, false)
	entry, err := store.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: owner.UserID, Delta: 125, ExpectedVersion: cfg.Version, RequestID: "currency-admin-render-check", Reason: "有流水账号的页面渲染"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://example.test/admin/currency/?username=owner", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("populated ledger: %d %s", w.Code, w.Body.String())
	}
	location, err := time.LoadLocation(s.siteSettings.Snapshot().Settings.Identity.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"有流水账号的页面渲染", "1.25", entry.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"), entry.CreatedAt.In(location).Format("2006-01-02 15:04"), "data-currency-admin-history"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("ledger missing %q", want)
		}
	}
}

func TestCurrencyHTTPTransferReceiptsAndSettings(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	store, cfg := currencyHTTPStore(t, s, false)
	receiver, receiverCookie, _ := createAndLoginTestUser(t, manager, owner, "coin-recipient", "coin-recipient@example.test", "收款人", "subscriber")
	headers := authenticatedAPIHeaders(owner)
	adjust := fmt.Sprintf(`{"user_id":"%d","delta":"900719925474099.31","reason":"精确金额","request_id":"http-exact-large-amount","version":"%d"}`, owner.UserID, cfg.Version)
	r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/currencies/main/adjust", adjust, headers)
	if r.Code != 201 {
		t.Fatal(r.Code, r.Body.String())
	}
	var entry currencyHistoryItem
	decodeAPIResponse(t, r, &entry)
	if entry.Delta != "900719925474099.31" || entry.Balance != entry.Delta {
		t.Fatal("money precision lost", entry)
	}
	lookup := serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/social/wallets/main/recipient?username=coin-recipient", "", nil)
	if lookup.Code != 200 || strings.Contains(lookup.Body.String(), "balance") || strings.Contains(lookup.Body.String(), "email") {
		t.Fatal("recipient lookup privacy", lookup.Body.String())
	}
	transfer := fmt.Sprintf(`{"recipient_id":"%d","amount":"1.25","request_id":"http-transfer-confirmed","version":"%d"}`, receiver.ID, cfg.Version)
	r = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/social/wallets/main/transfer", transfer, headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var result struct {
		Entry    currencyHistoryItem `json:"entry"`
		Replayed bool                `json:"replayed"`
	}
	decodeAPIResponse(t, r, &result)
	firstID := result.Entry.ID
	if result.Replayed || result.Entry.Delta != "-1.25" || result.Entry.Balance != "900719925474098.06" {
		t.Fatal(result)
	}
	if strings.Contains(r.Body.String(), `"received"`) || strings.Contains(r.Body.String(), `"balance":"1.25"`) {
		t.Fatal("recipient balance disclosed", r.Body.String())
	}
	// Completed receipts remain available even after the recipient is renamed/disabled.
	_, err := manager.UpdateUser(t.Context(), owner, receiver.ID, receiver.Version, auth.UserInput{Username: "renamed-recipient", Email: receiver.Email, DisplayName: receiver.DisplayName, Status: auth.UserStatusSuspended, Locale: "zh-CN", RoleSlugs: []string{"subscriber"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	cfg, err = store.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/social/wallets/main/transfer", transfer, headers)
	decodeAPIResponse(t, r, &result)
	if r.Code != 200 || !result.Replayed || result.Entry.ID != firstID {
		t.Fatal("completed transfer could not be confirmed", r.Code, r.Body.String())
	}
	assertAPIError(t, serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/social/wallets/main/transfer", strings.Replace(transfer, "1.25", "1.26", 1), headers), 409, "currency.request_reused")
	if balance, _ := store.Balance(t.Context(), receiver.ID); balance != 125 {
		t.Fatal("retry credited twice", balance)
	}
	if r = serveContentAPI(handler, receiverCookie, "GET", "https://example.test/api/v1/social/wallets/main", "", nil); r.Code != 401 {
		t.Fatal("disabled account still authenticated", r.Code)
	}
	changed := currencySettingsView(cfg)
	changed.Precision = 3
	changed.MaxBalance = virtualcurrency.FormatAmount(cfg.MaxBalance, 3)
	r = serveContentAPI(handler, cookie, "PUT", "https://example.test/api/v1/admin/currencies/main", currencyJSON(t, map[string]any{"settings": changed}), headers)
	assertAPIError(t, r, 409, "currency.precision_locked")
	changed = currencySettingsView(cfg)
	changed.Version = "1"
	assertAPIError(t, serveContentAPI(handler, cookie, "PUT", "https://example.test/api/v1/admin/currencies/main", currencyJSON(t, map[string]any{"settings": changed}), headers), 409, "currency.settings_conflict")
	changed = currencySettingsView(cfg)
	changed.Name, changed.Symbol = "社区币", "枚"
	r = serveContentAPI(handler, cookie, "PUT", "https://example.test/api/v1/admin/currencies/main", currencyJSON(t, map[string]any{"settings": changed}), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var resultSettings struct {
		Settings currencySettingsDTO `json:"settings"`
	}
	decodeAPIResponse(t, r, &resultSettings)
	saved := resultSettings.Settings
	if saved.Name != "社区币" || !saved.PrecisionLocked {
		t.Fatal(saved)
	}
}

func TestCurrencyHTTPAdminFormReplay(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	store, cfg := currencyHTTPStore(t, s, false)
	r := serveContentAPI(handler, cookie, "GET", "https://example.test/admin/currency/", "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "data-currency-admin") || !strings.Contains(r.Body.String(), "/css/chunnel-currency.css") {
		t.Fatal(r.Code, r.Body.String())
	}
	form := url.Values{"action": {"adjust"}, "user_id": {strconv.FormatInt(owner.UserID, 10)}, "delta": {"2.50"}, "reason": {"补发奖励"}, "version": {strconv.FormatInt(cfg.Version, 10)}, "request_id": {"html-currency-request-0001"}, "csrf_token": {owner.CSRFToken}}
	post := func(values url.Values) *httptest.ResponseRecorder {
		return serveContentAPI(handler, cookie, "POST", "https://example.test/admin/currency/", values.Encode(), map[string]string{"Origin": "https://example.test", "Content-Type": "application/x-www-form-urlencoded"})
	}
	for range 2 {
		if r = post(form); r.Code != 303 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if balance, err := store.Balance(t.Context(), owner.UserID); err != nil || balance != 250 {
		t.Fatal("HTML resubmission changed balance", balance, err)
	}
	form.Set("delta", "3.50")
	if r = post(form); r.Code != 409 {
		t.Fatal("changed replay accepted", r.Code)
	}
	form.Set("request_id", "")
	if r = post(form); r.Code != 422 {
		t.Fatal("server generated a fresh ID on replay", r.Code)
	}
	form.Set("request_id", "html-currency-request-0002")
	form.Add("delta", "2.50")
	if r = post(form); r.Code != 400 {
		t.Fatal("duplicate form value accepted", r.Code)
	}
}

func TestCurrencyHTTPRegistrationAndCheckInUseServerFacts(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	store, cfg := currencyHTTPStore(t, s, true)
	item, _, err := s.currencyCatalog.Create(t.Context(), virtualcurrency.CreateCurrency{Key: "gold", Name: "金币", Symbol: "G", Precision: 2, ActorID: owner.UserID, RequestID: "http-registration-create-gold"})
	if err != nil {
		t.Fatal(err)
	}
	gold, err := s.currencyCatalog.Store(t.Context(), item.Key)
	if err != nil {
		t.Fatal(err)
	}
	goldCfg := item.Settings
	goldCfg.Enabled, goldCfg.RegistrationEnabled, goldCfg.RegistrationReward = true, true, 200
	goldCfg, err = gold.UpdateSettings(t.Context(), goldCfg.Version, goldCfg)
	if err != nil {
		t.Fatal(err)
	}
	mailbox := &accessMailbox{}
	s.mailSender = mailbox
	setRegistration(t, s, "open")
	headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json"}
	base := "https://example.test/api/v1/social/access"
	r := serveContentAPI(handler, nil, "POST", base+"/request", `{"kind":"register","email":"reward-reader@example.test"}`, headers)
	if r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	token, kind := accessMailToken(t, mailbox)
	requestID, _ := auth.RandomCredential()
	signup := registrationBrowser(t, handler)
	complete := fmt.Sprintf(`{"request_id":%q,"kind":%q,"token":%q,"username":"reward-reader","display_name":"奖励读者","password":"Reward reader password 2026","confirm_password":"Reward reader password 2026"}`, requestID, kind, token)
	r = serveContentAPI(handler, signup, "POST", base+"/register", complete, headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	u, err := manager.FindUserByUsername(t.Context(), "reward-reader")
	if err != nil {
		t.Fatal(err)
	}
	if balance, err := store.Balance(t.Context(), u.ID); err != nil || balance != cfg.RegistrationReward {
		t.Fatal("registration reward missing", balance, err)
	}
	if balance, err := gold.Balance(t.Context(), u.ID); err != nil || balance != goldCfg.RegistrationReward {
		t.Fatal("registration did not apply the second currency's own reward", balance, err)
	}
	login, principal, err := manager.Authenticate("reward-reader-test", "reward-reader", "Reward reader password 2026")
	if err != nil {
		t.Fatal(err)
	}
	readerCookie := &http.Cookie{Name: auth.CookieName, Value: login}
	readerHeaders := authenticatedAPIHeaders(principal)
	for range 2 {
		r = serveContentAPI(handler, readerCookie, "POST", "https://example.test/api/v1/social/wallets/main/sync", "{}", readerHeaders)
		var result struct {
			Awarded     bool   `json:"awarded"`
			CurrencyKey string `json:"currency_key"`
		}
		decodeAPIResponse(t, r, &result)
		if r.Code != 200 || result.Awarded || result.CurrencyKey != "main" {
			t.Fatal("sync awarded registration twice", r.Code, r.Body.String())
		}
		r = serveContentAPI(handler, readerCookie, "POST", "https://example.test/api/v1/social/wallets/gold/sync", "{}", readerHeaders)
		decodeAPIResponse(t, r, &result)
		if r.Code != 200 || result.Awarded || result.CurrencyKey != "gold" {
			t.Fatal("scoped sync awarded registration twice", r.Code, r.Body.String())
		}
	}
	checkin := fmt.Sprintf(`{"version":"%d","request_id":"http-checkin-stable-0001"}`, cfg.Version)
	for range 2 {
		r = serveContentAPI(handler, readerCookie, "POST", "https://example.test/api/v1/social/wallets/main/check-in", checkin, readerHeaders)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if balance, _ := store.Balance(t.Context(), u.ID); balance != cfg.RegistrationReward+cfg.CheckInReward {
		t.Fatal("duplicate check-in", balance)
	}
	if balance, err := gold.Balance(t.Context(), u.ID); err != nil || balance != goldCfg.RegistrationReward {
		t.Fatal("main check-in or repeated sync changed the gold balance", balance, err)
	}
	for _, forged := range []string{`{"user_id":"1"}`, `{"verified_email":true}`, `{"occurred_at":"2026-12-01T00:00:00Z"}`, `{"amount":"100000"}`} {
		assertAPIError(t, serveContentAPI(handler, readerCookie, "POST", "https://example.test/api/v1/social/wallets/main/sync", forged, readerHeaders), 400, "request.invalid_json")
	}
	assertAPIError(t, serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/social/wallets/main/check-in", checkin, authenticatedAPIHeaders(owner)), 403, "currency.reward_ineligible")
	if balance, _ := store.Balance(t.Context(), owner.UserID); balance != 0 {
		t.Fatal("unverified pre-existing account rewarded", balance)
	}
}
