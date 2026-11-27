package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/shop"
)

// Real pages and reads from disposable stores; every browser business write is intercepted.
func TestShopStorefrontBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_SHOP_STOREFRONT_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated shop storefront and coupon browser fixture")
	}
	shopFrontendBrowserFixture(t, directory, false)
}

func shopFrontendBrowserFixture(t *testing.T, directory string, admin bool) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, users, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "shop-storefront-reader", "shop-storefront-reader@example.test", "商城读者", "subscriber")
	commerceService, _, _ := commerceHTTPStore(t, s)
	paths := walletBrowserStores(t, s, owner.UserID)
	shopPath := filepath.Join(t.TempDir(), "shop.json")
	backend, err := shop.NewFileBackend(shopPath)
	if err != nil {
		t.Fatal(err)
	}
	service := &shop.Service{Backend: backend, Currencies: s.currencyCatalog, Members: s.progression.Store, Users: s.auth}
	fixedNow := time.Now()
	service.Now = func() time.Time { return fixedNow }
	catalog := shop.InitialState().Catalog
	catalog.Enabled = true
	guide := createWidgetPost(t, repository, content.PostInput{AuthorID: owner.UserID, Title: "调色预设使用说明", Slug: "preset-guide", Status: content.StatusPublish, ContentSource: "下载资源后请先阅读随附的使用说明，再导入预设。不同照片的曝光和白平衡可能需要单独调整。"})
	makeProduct := func(key, title, kind, mode, cover string, price int64) shop.Product {
		return shop.Product{Key: key, Title: title, Summary: "测试商品，用于核对购买、付款与交付流程。", Kind: kind, Mode: mode, Enabled: true, Currency: "CNY", Price: price, Stock: 80, Image: "/theme-assets/images/editorial/" + cover + "-small.webp", Category: "测试分类", Delivery: "测试交付内容：感谢购买，资源链接将在这里展示。"}
	}
	a := makeProduct("digital", "摄影调色预设", "virtual", "direct", "cover-library", 3900)
	a.Summary = "个人创作与工作室授权，可按使用范围选择。"
	a.Description = "包含多种场景的调色预设。\n个人版用于个人创作，工作室版可用于团队协作。\n付清后可在订单详情中获取文件与使用说明。"
	faq := articlewidgets.Config{Version: 1, ID: "product_usage_faq", Kind: "accordion", Title: "使用与授权", Items: []articlewidgets.Item{{Title: "如何下载文件？", Text: "付款完成后，在我的订单中查看下载内容与使用说明。"}, {Title: "如何选择授权？", Text: "个人创作选择个人版；团队协作请选择工作室授权版。"}}}
	a.DescriptionSource = "<p>包含多种场景的调色预设，可按使用范围选择个人版或工作室版。</p>" + componentSource(faq) + fmt.Sprintf(`<aside data-np-widget="post" data-np-post="%d" data-np-layout="compact"><p><a href="/posts/preset-guide">调色预设使用说明</a></p></aside>`, guide.ID)
	a.DescriptionAuthorID = owner.UserID
	a.CompareAt = 12900
	a.Images = []string{"/theme-assets/images/editorial/cover-print-small.webp", "/theme-assets/images/editorial/cover-notes-small.webp", "/theme-assets/images/editorial/cover-community-small.webp", "/theme-assets/images/editorial/cover-stream-small.webp"}
	a.DetailImages = []string{"/theme-assets/images/editorial/cover-community-small.webp"}
	a.Variants = []shop.Variant{{Key: "personal", Title: "个人创作版", Image: a.Image, Price: 3900, Stock: 30}, {Key: "studio", Title: "工作室授权版", Image: a.Images[0], Price: 9900, Stock: 20}}
	b := makeProduct("physical", "手作陶杯", "physical", "direct", "cover-print", 12900)
	b.ShippingFee = 600
	code := makeProduct("cdk", "工具箱激活码", "cdk", "direct", "cover-notes", 4900)
	code.Codes = []string{"TEST-KEY-001", "TEST-KEY-002", "TEST-KEY-003"}
	donation := makeProduct("donation", "捐赠", "donation", "direct", "reading-room", 500)
	donation.MaxDonation = 100000
	raffle := makeProduct("raffle", "即时抽奖码", "raffle", "direct", "cover-community", 100)
	raffle.Raffle = &shop.RafflePlan{Mode: "instant", WinBPS: 10000, Winners: 1, Prize: "一份创作者数字礼物"}
	draw := makeProduct("draw", "定时抽奖码", "raffle", "direct", "cover-stream", 100)
	draw.Raffle = &shop.RafflePlan{Mode: "scheduled", DrawAt: time.Now().Add(time.Hour), Winners: 2, Prize: "周末纪念套装"}
	bundle := makeProduct("bundle", "陶杯与工具箱套装", "bundle", "direct", "cover-notes", 15900)
	bundle.Bundle = []shop.BundleItem{{Product: b.Key, Quantity: 1}, {Product: code.Key, Quantity: 1}}
	presale := makeProduct("presale", "城市影像集预售", "virtual", "presale", "cover-library", 6900)
	presale.ReleaseAt = time.Now().Add(24 * time.Hour)
	deposit := makeProduct("deposit", "装帧画册", "physical", "deposit", "cover-print", 29900)
	deposit.Deposit = 4900
	deposit.ReleaseAt = time.Now().Add(time.Hour)
	deposit.BalanceDueAt = time.Now().Add(48 * time.Hour)
	installment := makeProduct("installment", "摄影设备套装", "physical", "installment", "cover-stream", 129900)
	installment.Installment = &shop.InstallmentPlan{Periods: []int{3, 6, 12}, IntervalDays: 30, InterestBPS: 50}
	group := makeProduct("group", "双人拼团电子书", "virtual", "group", "cover-community", 5900)
	group.Group = &shop.GroupPlan{Size: 2, Price: 3900, Hours: 24}
	group.Services = &shop.ServiceModules{Mode: "custom"}
	for _, key := range []string{"order-delivery", "currency", "group-size", "group-time", "refund", "buyer-reviews"} {
		for _, preset := range shop.ServiceModuleLibrary().Templates {
			if preset.Template == key {
				group.Services.Items = append(group.Services.Items, preset.ServiceModule)
			}
		}
	}
	catalog.Products = []shop.Product{a, b, code, donation, raffle, draw, bundle, presale, deposit, installment, group}
	points := makeProduct("points", "积分数字资料", "virtual", "direct", "cover-library", 10)
	points.Currency = "wallet:main"
	points.Precision = 2
	catalog.Products = append(catalog.Products, points)
	for i := range catalog.Products {
		catalog.Products[i].Category = map[string]string{"virtual": "数字资源", "physical": "生活好物", "cdk": "工具软件", "donation": "捐赠", "raffle": "抽奖活动", "bundle": "组合套装"}[catalog.Products[i].Kind]
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
	adminOrders := map[string]string{}
	if admin {
		adminOrders = seedShopAdminBrowser(t, service, owner.UserID)
	}
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
		commerceState, err := commerceService.Backend.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		state := map[string]any{"accounts": accounts, "settings": s.siteSettings.Snapshot(), "commerce": commerceState}
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
	if admin {
		registerShopAdminBrowserResponses(t, mux, s, service, paths[0], owner.UserID)
	}
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

	// Read-only fixture response generation runs on a fresh memory clone. Browser POSTs never reach business handlers.
	mux.HandleFunc("GET /fixture/response/{operation}", func(w http.ResponseWriter, r *http.Request) {
		original, err := backend.Read(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		encoded, err := json.Marshal(original)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		var state shop.State
		if err = json.Unmarshal(encoded, &state); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		isolated, err := shop.NewFileBackend("")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err = isolated.Update(r.Context(), func(next *shop.State) error { *next = state; return nil }); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		clone := *service
		clone.Backend = isolated
		user := owner.UserID
		if r.URL.Query().Get("role") == "reader" {
			user = reader.UserID
		}
		switch r.PathValue("operation") {
		case "quote":
			var input shop.QuoteInput
			if err = json.Unmarshal([]byte(r.URL.Query().Get("input")), &input); err != nil {
				http.Error(w, err.Error(), 422)
				return
			}
			quote, err := clone.QuoteFor(r.Context(), user, input)
			if err != nil {
				s.shopError(w, r, err)
				return
			}
			writeJSON(w, 200, map[string]any{"quote": quote})
		case "create":
			var input shop.Purchase
			if err = json.Unmarshal([]byte(r.URL.Query().Get("input")), &input); err != nil {
				http.Error(w, err.Error(), 422)
				return
			}
			input.UserID = user
			order, err := clone.Create(r.Context(), input)
			if err != nil {
				s.shopError(w, r, err)
				return
			}
			writeJSON(w, 200, map[string]any{"order": shop.PublicOrder(order)})
		default:
			http.NotFound(w, r)
		}
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
	writeEvidence("fixture.json", map[string]any{"url": host.URL, "login": host.URL + "/fixture/login/owner", "owner_id": owner.UserID, "reader_id": reader.UserID, "orders": adminOrders})
	t.Log("shop storefront fixture ready", host.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(40 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("shop storefront fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(directory, "stop")); err == nil {
				after := readState()
				if !reflect.DeepEqual(before, after) || blocked.Load() != 0 {
					t.Fatalf("fixture changed or %d writes escaped interception", blocked.Load())
				}
				digest := sha256.Sum256(after)
				writeEvidence("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "currency_and_progression_ledgers_unchanged": true, "commerce_unchanged": true, "shop_catalog_orders_coupons_claims_unchanged": true, "settings_unchanged_except_fixture_style": true, "unintercepted_mutations": blocked.Load(), "state_sha256": hex.EncodeToString(digest[:])})
				return
			}
		}
	}
}
