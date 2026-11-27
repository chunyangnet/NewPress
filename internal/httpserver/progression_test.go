package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/virtualcurrency"
)

func progressionHTTPStore(t *testing.T, s *Server) (*progression.Store, progression.Settings, *virtualcurrency.FileStore) {
	t.Helper()
	coin, _ := currencyHTTPStore(t, s, false)
	store, err := progression.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := progression.DefaultSettings()
	cfg.Experience.Enabled = true
	cfg.Experience.RequireVerifiedEmail = false
	cfg.CheckIn.Enabled = true
	cfg.Levels.Enabled = true
	cfg.Membership.Enabled = true
	cfg.Membership.Plans[0].PurchaseEnabled = true
	cfg.Membership.Plans[0].Price = "1.25"
	cfg, err = store.UpdateSettings(t.Context(), 1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.SetProgressionService(&progression.Service{Store: store, Users: s.auth, Content: s.repository, Currencies: s.currencyCatalog})
	return store, cfg, coin
}
func TestProgressionHTTPPermissionsStrictPayloadsAndPrivacy(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	store, cfg, _ := progressionHTTPStore(t, s)
	_, readerCookie, reader := createAndLoginTestUser(t, manager, owner, "progression-reader", "progression-reader@example.test", "成长读者", "subscriber")
	for _, path := range []string{"/api/v1/social/progression", "/api/v1/admin/progression", "/api/v1/admin/progression/account?username=owner"} {
		r := serveContentAPI(handler, nil, "GET", "https://example.test"+path, "", nil)
		if r.Code != 401 {
			t.Fatal(path, r.Code, r.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/admin/progression", "/api/v1/admin/progression/account?username=owner", "/admin/progression/"} {
		r := serveContentAPI(handler, readerCookie, "GET", "https://example.test"+path, "", nil)
		if r.Code != 403 {
			t.Fatal(path, r.Code, r.Body.String())
		}
	}
	valid := fmt.Sprintf(`{"version":"%d","request_id":"progression-http-checkin"}`, cfg.Version)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"injected user", strings.TrimSuffix(valid, "}") + `,"user_id":"1"}`, 400},
		{"case alias", strings.Replace(valid, "version", "Version", 1), 400},
		{"null", strings.Replace(valid, `"request_id":"progression-http-checkin"`, `"request_id":null`, 1), 400},
		{"duplicate", strings.Replace(valid, `"version":`, `"version":"2","version":`, 1), 400},
		{"numeric version", strings.Replace(valid, `"version":"2"`, `"version":2`, 1), 400},
		{"missing version", `{"request_id":"progression-http-checkin"}`, 400},
		{"noncanonical", strings.Replace(valid, `"version":"2"`, `"version":"02"`, 1), 400},
		{"oversize", strings.Replace(valid, "progression-http-checkin", strings.Repeat("a", 132000), 1), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := serveContentAPI(handler, readerCookie, "POST", "https://example.test/api/v1/social/progression/check-in", tc.body, authenticatedAPIHeaders(reader))
			if r.Code != tc.status {
				t.Fatal(r.Code, r.Body.String())
			}
		})
	}
	for _, header := range []string{"Origin", "X-CSRF-Token", "Content-Type"} {
		r := httptest.NewRequest("POST", "https://example.test/api/v1/social/progression/check-in", strings.NewReader(valid))
		for key, value := range authenticatedAPIHeaders(reader) {
			r.Header.Set(key, value)
		}
		r.Header.Add(header, r.Header.Get(header))
		r.AddCookie(readerCookie)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		if out.Code != 403 && out.Code != 415 {
			t.Fatal(header, out.Code, out.Body.String())
		}
	}
	for _, origin := range []string{"", "https://attacker.test"} {
		headers := authenticatedAPIHeaders(reader)
		headers["Origin"] = origin
		r := serveContentAPI(handler, readerCookie, "POST", "https://example.test/api/v1/social/progression/check-in", valid, headers)
		if r.Code != 403 {
			t.Fatal(origin, r.Code)
		}
	}
	for _, path := range []string{"/api/v1/social/progression?user_id=1", "/api/v1/social/progression?before=1&before=2", "/api/v1/admin/progression?unknown=1"} {
		r := serveContentAPI(handler, cookie, "GET", "https://example.test"+path, "", nil)
		if r.Code != 400 {
			t.Fatal(path, r.Code)
		}
	}
	r := serveContentAPI(handler, readerCookie, "POST", "https://example.test/api/v1/social/progression/check-in", valid, authenticatedAPIHeaders(reader))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	r = serveContentAPI(handler, readerCookie, "GET", "https://example.test/api/v1/social/progression", "", nil)
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" || !strings.Contains(r.Header().Get("Vary"), "Cookie") {
		t.Fatal(r.Code, r.Header(), r.Body.String())
	}
	v, err := store.Snapshot(t.Context(), owner.UserID, 0)
	if err != nil || v.Account.Experience != 0 {
		t.Fatal("another user's experience changed", v, err)
	}
}

func TestHomeCheckInUsesAvailableRewardSource(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	store, cfg, coin := progressionHTTPStore(t, s)
	values := s.themes.SettingsSnapshot().Values
	values["home_checkin_visibility"] = "both"
	check := func(want string) {
		t.Helper()
		d, err := s.homePage(httptest.NewRequest("GET", "https://example.test/", nil), values)
		if err != nil || d.Home.CheckIn.Source != want {
			t.Fatalf("home check-in source = %q, want %q: %v", d.Home.CheckIn.Source, want, err)
		}
	}
	check("progression")
	cfg.Experience.Enabled = false
	if _, err := store.UpdateSettings(t.Context(), cfg.Version, cfg); err != nil {
		t.Fatal(err)
	}
	check("")
	rules, err := coin.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rules.CheckInEnabled = true
	if _, err = coin.UpdateSettings(t.Context(), rules.Version, rules); err != nil {
		t.Fatal(err)
	}
	check("wallet")
}
func TestProgressionHTTPModulesAndMembershipPurchases(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	store, cfg, coin := progressionHTTPStore(t, s)
	_, readerCookie, reader := createAndLoginTestUser(t, manager, owner, "member-reader", "member-reader@example.test", "会员读者", "subscriber")
	headers := authenticatedAPIHeaders(owner)
	check := cfg.CheckIn
	check.Amount = 30
	check.Mode = "calendar_day"
	check.Timezone = "Asia/Shanghai"
	r := serveContentAPI(handler, cookie, "PATCH", "https://example.test/api/v1/admin/progression/modules/check-in", currencyJSON(t, map[string]any{"version": fmt.Sprint(cfg.Version), "settings": check}), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	cfg, _ = store.Settings(t.Context())
	if cfg.CheckIn.Amount != 30 || cfg.CheckIn.Mode != "calendar_day" || cfg.CheckIn.Timezone != "Asia/Shanghai" || cfg.Publication.Amount != 50 {
		t.Fatal(cfg)
	}
	r = serveContentAPI(handler, cookie, "PATCH", "https://example.test/api/v1/admin/progression/modules/check-in", `{"version":"3","settings":{"amount":"40","interval_hours":24}}`, headers)
	if r.Code != 422 {
		t.Fatal("omitted enabled accepted", r.Code, r.Body.String())
	}
	c, _ := coin.Settings(t.Context())
	if _, err := coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: reader.UserID, Delta: 1000, ExpectedVersion: c.Version, RequestID: "http-member-fixture-fund", Reason: "测试会员余额"}); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"version":"%d","member_version":"0","plan":"member","mode":"grant","request_id":"http-membership-purchase"}`, cfg.Version)
	for _, extra := range []string{`,"amount":"0.01"}`, `,"days":3650}`, `,"payment_entry":"1"}`, `,"user_id":"1"}`} {
		r = serveContentAPI(handler, readerCookie, "POST", "https://example.test/api/v1/social/progression/purchase", strings.TrimSuffix(body, "}")+extra, authenticatedAPIHeaders(reader))
		if r.Code != 400 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	for range 2 {
		r = serveContentAPI(handler, readerCookie, "POST", "https://example.test/api/v1/social/progression/purchase", body, authenticatedAPIHeaders(reader))
		if r.Code != 200 || !strings.Contains(r.Body.String(), `"status":"completed"`) {
			t.Fatal(r.Code, r.Body.String())
		}
		for _, secret := range []string{"payment_request", "fingerprint", "terms_hash"} {
			if strings.Contains(r.Body.String(), secret) {
				t.Fatal("private payment data exposed", secret)
			}
		}
	}
	if balance, _ := coin.Balance(t.Context(), reader.UserID); balance != 875 {
		t.Fatal(balance)
	}
	v, err := store.Snapshot(t.Context(), reader.UserID, 0)
	if err != nil || v.Account.Membership.Version != 1 {
		t.Fatal(v, err)
	}
	grant := fmt.Sprintf(`{"user_id":"%d","version":"%d","member_version":"1","action":"revoke","plan":"","days":0,"request_id":"http-membership-revoke","reason":"测试撤销"}`, reader.UserID, cfg.Version)
	r = serveContentAPI(handler, readerCookie, "POST", "https://example.test/api/v1/admin/progression/membership", grant, authenticatedAPIHeaders(reader))
	if r.Code != 403 {
		t.Fatal(r.Code, r.Body.String())
	}
	r = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/progression/membership", grant, headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if p, err := manager.GetUser(t.Context(), reader.UserID); err != nil || len(p.Roles) != 1 || p.Roles[0].Slug != "subscriber" {
		t.Fatal("membership changed role", p, err)
	}
	r = serveContentAPI(handler, cookie, "GET", "https://example.test/admin/progression/", "", nil)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "data-progression-admin") {
		t.Fatal(r.Code, r.Body.String())
	}
}
func TestProgressionHTTPPendingOrderReadNeverCharges(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	store, cfg, coin := progressionHTTPStore(t, s)
	c, _ := coin.Settings(t.Context())
	in := progression.PurchaseInput{UserID: owner.UserID, ExpectedVersion: cfg.Version, MemberVersion: 0, Plan: "member", Mode: "grant", RequestID: "http-pending-membership"}
	order, err := store.BeginPurchase(t.Context(), in, progression.CurrencyQuote{Key: "main", Symbol: c.Symbol, Version: c.Version, Precision: c.Precision, Enabled: c.Enabled})
	if err != nil {
		t.Fatal(err)
	}
	r := serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/social/progression", "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"pending_order":{"user_id"`) {
		t.Fatal(r.Code, r.Body.String())
	}
	if strings.Contains(r.Body.String(), order.PaymentRequest) || strings.Contains(r.Body.String(), order.Fingerprint) || strings.Contains(r.Body.String(), order.TermsHash) {
		t.Fatal("private order terms leaked")
	}
	r = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/social/progression/sync", "{}", authenticatedAPIHeaders(owner))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if _, found, err := coin.SpendReceipt(t.Context(), order.Payment()); err != nil || found {
		t.Fatal("sync authorized payment", found, err)
	}
	r = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/social/progression/purchase/cancel", `{"request_id":"http-pending-membership"}`, authenticatedAPIHeaders(owner))
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"status":"cancelled"`) {
		t.Fatal(r.Code, r.Body.String())
	}
}
