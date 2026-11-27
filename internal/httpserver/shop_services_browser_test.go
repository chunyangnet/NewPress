package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/shop"
)

// Real pages and reads from disposable stores; every browser business write is intercepted.
func TestShopServicesBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_SHOP_SERVICES_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated shop service and coupon browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, users, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "shop-service-reader", "shop-service-reader@example.test", "商城读者", "subscriber")
	paths := walletBrowserStores(t, s, owner.UserID)
	shopPath := filepath.Join(t.TempDir(), "shop.json")
	backend, err := shop.NewFileBackend(shopPath)
	if err != nil {
		t.Fatal(err)
	}
	service := &shop.Service{Backend: backend, Currencies: s.currencyCatalog, Members: s.progression.Store, Users: s.auth}
	catalog := shop.InitialState().Catalog
	catalog.Enabled = true
	catalog.Products = []shop.Product{
		{Key: "digital", Title: "摄影调色预设", Kind: "virtual", Mode: "direct", Enabled: true, Currency: "CNY", Price: 3900, Stock: 80, Summary: "个人创作预设，付清后查看交付内容。", Delivery: "隔离验收交付内容"},
		{Key: "group", Title: "双人拼团电子书", Kind: "virtual", Mode: "group", Enabled: true, Currency: "CNY", Price: 5900, Stock: 80, Group: &shop.GroupPlan{Size: 2, Price: 3900, Hours: 24}, Services: &shop.ServiceModules{Mode: "custom"}},
		{Key: "physical", Title: "手作陶杯", Kind: "physical", Mode: "direct", Enabled: true, Currency: "CNY", Price: 12900, ShippingFee: 600, Stock: 80},
	}
	for _, key := range []string{"order-delivery", "currency", "group-size", "group-time", "refund", "buyer-reviews"} {
		for _, preset := range shop.ServiceModuleLibrary().Templates {
			if preset.Template == key {
				catalog.Products[1].Services.Items = append(catalog.Products[1].Services.Items, preset.ServiceModule)
			}
		}
	}
	if _, err = service.SaveCatalog(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	coupons := []shop.Coupon{
		{Code: "PHOTO-5", Title: "调色预设优惠券", Kind: "fixed", Value: 500, Currency: "CNY", Products: []string{"digital"}, PerUserLimit: 3, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour), Enabled: true, Home: true},
		{Code: "POINTS-12", Title: "积分结算优惠券", Kind: "fixed", Value: 12, Currency: "wallet:main", PerUserLimit: 1, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(48 * time.Hour), Enabled: true, Home: true},
		{Code: "MARKET-ONLY", Title: "仅在商城展示", Kind: "percent", Value: 1250, PerUserLimit: 1, StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour), Enabled: true, Home: false},
	}
	if _, err = service.SaveCoupons(t.Context(), 0, coupons); err != nil {
		t.Fatal(err)
	}
	s.SetShopService(service)
	values := s.themes.SettingsSnapshot().Values
	values["market_nav"], values["market_module"] = true, true
	if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	paths = append(paths, shopPath)
	readState := func() []byte {
		accounts, err := users.ListUsers(t.Context(), auth.UserQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		state := map[string]any{"accounts": accounts, "settings": s.siteSettings.Snapshot()}
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
		cookie := *ownerCookie
		if r.PathValue("role") == "reader" {
			cookie = *readerCookie
		}
		if r.PathValue("role") == "guest" {
			cookie.Value, cookie.MaxAge = "", -1
		}
		cookie.Secure = false
		http.SetCookie(w, &cookie)
		http.Redirect(w, r, "/", http.StatusSeeOther)
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
			http.Error(w, "fixture style", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
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
	writeEvidence("fixture.json", map[string]any{"url": host.URL, "login": host.URL + "/fixture/login/owner", "owner_id": owner.UserID, "reader_id": reader.UserID})
	t.Log("shop service fixture ready", host.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(40 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("shop service fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(directory, "stop")); err == nil {
				after := readState()
				if !reflect.DeepEqual(before, after) || blocked.Load() != 0 {
					t.Fatalf("fixture changed or %d writes escaped interception", blocked.Load())
				}
				digest := sha256.Sum256(after)
				writeEvidence("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "currency_and_progression_ledgers_unchanged": true, "shop_catalog_orders_coupons_claims_unchanged": true, "settings_unchanged_except_fixture_style": true, "unintercepted_mutations": blocked.Load(), "state_sha256": hex.EncodeToString(digest[:])})
				return
			}
		}
	}
}
