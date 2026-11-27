package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/shop"
)

// Opt-in isolated browser fixture. No existing site's users, keys or inventory are opened.
func TestShopBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_SHOP_BROWSER_FIXTURE")
	if dir == "" {
		t.Skip("isolated shop browser QA")
	}
	dir, _ = filepath.Abs(dir)
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	s, handler, _, repository, cookie, owner := newPersistentAdminTestServer(t)
	guide := createWidgetPost(t, repository, content.PostInput{AuthorID: owner.UserID, Title: "调色预设使用说明", Slug: "preset-guide", Status: content.StatusPublish, ContentSource: "下载资源后请先阅读随附的使用说明，再导入预设。不同照片的曝光和白平衡可能需要单独调整。"})
	paymentService, _, _ := commerceHTTPStore(t, s)
	backend, e := shop.NewFileBackend(filepath.Join(t.TempDir(), "shop.json"))
	if e != nil {
		t.Fatal(e)
	}
	service := &shop.Service{Backend: backend}
	c := shop.InitialState().Catalog
	c.Enabled = true
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
	c.Products = []shop.Product{a, b, code, donation, raffle, draw, bundle, presale, deposit, installment, group}
	for i := range c.Products {
		c.Products[i].Category = map[string]string{"virtual": "数字资源", "physical": "生活好物", "cdk": "工具软件", "donation": "捐赠", "raffle": "抽奖活动", "bundle": "组合套装"}[c.Products[i].Kind]
	}
	if _, e = service.SaveCatalog(t.Context(), c); e != nil {
		t.Fatal(e)
	}
	if _, e = service.SaveCoupons(t.Context(), 0, []shop.Coupon{{Code: "PHOTO-5", Title: "调色预设优惠券", Kind: "fixed", Value: 500, Currency: "CNY", Products: []string{a.Key}, PerUserLimit: 1, StartsAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(24 * time.Hour), Enabled: true, Home: true}}); e != nil {
		t.Fatal(e)
	}
	// Real completed purchases in this isolated fixture exercise review rendering.
	for index, score := range []int{5, 4, 3} {
		catalog, _ := service.Catalog(t.Context())
		order, err := service.Create(t.Context(), shop.Purchase{QuoteInput: shop.QuoteInput{Product: a.Key, Variant: "personal", Quantity: 1}, UserID: int64(index + 10), Version: catalog.Version, RequestID: fmt.Sprintf("review-fixture-%d", index), Accepted: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.MarkPaid(t.Context(), shop.PaymentReceipt{OrderID: order.ID, UserID: order.UserID, Index: 0, Reference: fmt.Sprintf("review-fixture-paid-%d", index), Amount: order.Total, Currency: order.Currency}); err != nil {
			t.Fatal(err)
		}
		review, err := service.CreateReview(t.Context(), order.UserID, order.ID, []string{"小林", "山间来信", "晴天"}[index], shop.ReviewInput{Rating: score, Content: []string{"下载很顺利，预设分类清楚。室内照片稍微调整曝光就能使用。", "工作室常用的色调都有，说明也比较详细。希望增加夜景示例。", "文件已收到，部分照片需要手动微调。"}[index], Anonymous: index == 2})
		if err != nil {
			t.Fatal(err)
		}
		if index == 1 {
			if _, err = service.ModerateReview(t.Context(), 1, review.ID, shop.ReviewModeration{Version: 1, Reply: "谢谢你的建议，夜景示例会随下次内容更新补充。"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	s.SetShopService(service)
	paymentService.SettleShop = func(ctx context.Context, p commerce.ShopSettlement) error {
		_, e := service.MarkPaid(ctx, shop.PaymentReceipt{OrderID: p.OrderID, UserID: p.UserID, Index: p.Installment - 1, Reference: p.PaymentID, Amount: p.Amount, Currency: p.Currency})
		return e
	}
	values := s.themes.SettingsSnapshot().Values
	values["market_nav"] = true
	values["market_module"] = true
	if _, fields, e := s.themes.SaveSettings(values); e != nil || len(fields) > 0 {
		t.Fatal(e, fields)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		copy := *cookie
		copy.Secure = false
		http.SetCookie(w, &copy)
		http.Redirect(w, r, "/shop/", 303)
	})
	mux.HandleFunc("POST /fixture/style/{style}", func(w http.ResponseWriter, r *http.Request) {
		style := r.PathValue("style")
		if style != "classic" && style != "liquid-glass" {
			http.NotFound(w, r)
			return
		}
		v := s.themes.SettingsSnapshot().Values
		v["visual_style"] = style
		if _, fields, e := s.themes.SaveSettings(v); e != nil || len(fields) > 0 {
			http.Error(w, "style", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /fixture/paid/{id}", func(w http.ResponseWriter, r *http.Request) {
		o, e := paymentService.Order(r.Context(), 0, r.PathValue("id"))
		if e != nil {
			http.NotFound(w, r)
			return
		}
		body := epayHTTPNotificationTrade(o.ID, commerce.PublicOrder(o)["amount"].(string), "fixture-merchant-secret", "fixture-"+o.ID)
		response := serveContentAPI(handler, nil, "POST", s.config.PublicURL+"/api/v1/payments/notify/"+o.ID, body, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	})
	mux.Handle("/", handler)
	host := httptest.NewUnstartedServer(mux)
	if port := os.Getenv("NEWPRESS_SHOP_BROWSER_PORT"); port != "" {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			host.Listener.Close()
			t.Fatal(err)
		}
		host.Listener.Close()
		host.Listener = listener
	}
	host.Start()
	defer host.Close()
	s.config.PublicURL = host.URL
	// The prepared checkout uses the reserved HTTPS test domain. Browser QA
	// never opens that external checkout; /fixture/paid verifies its signed callback.
	paymentService.PublicURL = "https://example.test"
	data, _ := json.Marshal(map[string]string{"url": host.URL, "login": host.URL + "/fixture/login"})
	if e = os.WriteFile(filepath.Join(dir, "fixture.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-ticker.C:
			if _, e = os.Stat(filepath.Join(dir, "stop")); e == nil {
				return
			}
		}
	}
}
