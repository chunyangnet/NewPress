package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/payment"
	"newpress.local/newpress/internal/virtualcurrency"
	"strings"

	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
)

// Opt-in real pages and reads, disposable accounts and stores. The browser
// intercepts every business write, including all payment provider operations.
func TestPaymentsBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_PAYMENTS_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated payments and billing browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, users, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "payment-reader", "payment-reader@example.test", "支付读者", "subscriber")
	paths := walletBrowserStores(t, s, owner.UserID)
	storeDir := filepath.Dir(paths[0])
	catalog, err := virtualcurrency.NewFileCatalog(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = catalog.Create(t.Context(), virtualcurrency.CreateCurrency{Key: "stars", Name: "星星", Symbol: "星", Precision: 0, ActorID: owner.UserID, RequestID: "payment-fixture-stars"}); err != nil {
		t.Fatal(err)
	}
	star, err := catalog.Store(t.Context(), "stars")
	if err != nil {
		t.Fatal(err)
	}
	starSettings, err := star.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	starSettings.Enabled = true
	if _, err = star.UpdateSettings(t.Context(), starSettings.Version, starSettings); err != nil {
		t.Fatal(err)
	}
	if err = s.SetCurrencyCatalog(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	s.progression.Currencies = catalog
	backend, err := commerce.NewFileBackend(filepath.Join(storeDir, "commerce.json"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := commerce.NewVault(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Seal(payment.Config{Provider: "epay", Endpoint: "https://payments.example.test", MerchantID: "fixture-merchant", Secret: "fixture-only-secret", PaymentType: "alipay"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := commerce.Defaults()
	cfg.Version = 2
	cfg.Enabled = true
	cfg.RequireVerifiedEmail = false
	cfg.Channels = []commerce.Channel{{Key: "epay", Name: "隔离测试渠道", Provider: "epay", Currency: "CNY", Enabled: true, SealedConfig: sealed}}
	member, err := s.progression.Store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan := member.Membership.Plans[0]
	cfg.Products = []commerce.Product{{Key: "coins", Title: "钱包充值", Enabled: true, Kind: "recharge", Currency: "CNY", Price: 1000, TargetCurrency: "main", Credit: 500}, {Key: "member", Title: "会员购买", Enabled: true, Kind: "membership", Currency: "CNY", Price: 1000, Plan: plan.Key}}
	cfg.Exchanges = []commerce.Exchange{{Key: "stars", Title: "兑换星星", Enabled: true, From: "main", To: "stars", FromUnits: 100, ToUnits: 3, FeeBPS: 250, Minimum: 1, Maximum: 1000000}}
	now := time.Now().UTC()
	order := commerce.Order{ID: "np_0123456789abcdef0123456789ab", UserID: owner.UserID, RequestID: "payment-fixture-original", Fingerprint: "fixture", Kind: "recharge", Title: "钱包充值", Product: "coins", Channel: "epay", Provider: "epay", PaymentScope: "fixture", SealedConfig: sealed, Status: "pending", Amount: 1000, Currency: "CNY", Precision: 2, TargetCurrency: "main", TargetPrecision: 2, Credit: 500, SettingsVersion: 2, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(30 * time.Minute), Disclaimer: cfg.Disclaimer, RefundPolicy: cfg.RefundPolicy, Checkout: payment.Checkout{ProviderOrderID: "fixture-provider", URL: "https://payments.example.test/checkout", QRCode: "https://payments.example.test/qr"}}
	shop := order
	shop.ID = "np_ffffffffffffffffffffffffffff"
	shop.RequestID = "payment-fixture-shop"
	shop.Kind = "shop"
	shop.ShopOrder = "fixture-shop"
	shop.ShopInstallment = 1
	shop.Title = "商城订单"
	if err = backend.Update(t.Context(), func(state *commerce.State) error {
		state.Settings = cfg
		state.Orders[order.ID] = order
		state.Orders[shop.ID] = shop
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.SetCommerceService(&commerce.Service{Backend: backend, Vault: vault, Users: s.auth, Members: s.progression.Store, Currencies: catalog, PublicURL: "https://example.test", ProviderFactory: func(payment.Config) (payment.Provider, error) { return nil, payment.ErrUnsupported }})
	paths = nil
	if err = filepath.WalkDir(storeDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".json") {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	readState := func() []byte {
		accounts, err := users.ListUsers(t.Context(), auth.UserQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		state := map[string]any{"accounts": accounts, "site_settings": s.siteSettings.Snapshot()}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			state[filepath.Base(path)] = string(data)
		}
		values := s.themes.SettingsSnapshot().Values
		delete(values, "visual_style")
		state["theme_settings"] = values
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := readState()
	var blocked atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login/{role}", func(w http.ResponseWriter, r *http.Request) {
		cookie := ownerCookie
		if r.PathValue("role") == "reader" {
			cookie = readerCookie
		} else if r.PathValue("role") == "guest" {
			expired := *ownerCookie
			expired.Value, expired.MaxAge = "", -1
			cookie = &expired
		}
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/payments/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /fixture/style/{style}", func(w http.ResponseWriter, r *http.Request) {
		style := r.PathValue("style")
		if style != "classic" && style != "liquid-glass" {
			http.NotFound(w, r)
			return
		}
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) > 0 {
			http.Error(w, "cannot change fixture style", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" && !(r.Method == "POST" && r.URL.Path == "/api/v1/admin/payments/preview") {
			blocked.Add(1)
			http.Error(w, "browser mutation was not intercepted", 405)
			return
		}
		handler.ServeHTTP(w, r)
	})
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	writeEvidence := func(name string, value any) {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeEvidence("fixture.json", map[string]any{"url": host.URL, "login": host.URL + "/fixture/login/owner", "owner_id": owner.UserID, "reader_id": reader.UserID, "username": owner.Username, "order_id": order.ID, "shop_order_id": shop.ID, "plan": plan.Key})
	t.Log("payments browser fixture ready", host.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(40 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("payments browser fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(directory, "stop")); err == nil {
				after := readState()
				if !reflect.DeepEqual(before, after) || blocked.Load() != 0 {
					t.Fatalf("fixture state changed or %d writes escaped browser interception", blocked.Load())
				}
				digest := sha256.Sum256(after)
				writeEvidence("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "currency_accounts_ledger_and_receipts_unchanged": true, "progression_accounts_entries_receipts_and_orders_unchanged": true, "settings_unchanged": true, "commerce_orders_and_settings_unchanged": true, "theme_settings_unchanged_except_fixture_style": true, "unintercepted_mutations": blocked.Load(), "state_sha256": hex.EncodeToString(digest[:])})
				return
			}
		}
	}
}
