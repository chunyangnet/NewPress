package httpserver

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"newpress.local/newpress/internal/shop"
)

func TestShopHTTPRafflePersonalQuotaAndAnonymousOverview(t *testing.T) {
	s, handler, manager, _, _, admin := newPersistentAdminTestServer(t)
	commerceHTTPStore(t, s)
	backend, err := shop.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	service := &shop.Service{Backend: backend}
	c := shop.InitialState().Catalog
	c.Enabled = true
	c.RaffleCampaigns = []shop.RaffleCampaign{{Key: "shared", Title: "共享抽奖", TicketLimit: 10, UserLimit: 3}}
	for _, key := range []string{"a", "b"} {
		c.Products = append(c.Products, shop.Product{Key: key, Title: "抽奖 " + key, Kind: "raffle", Mode: "direct", Enabled: true, Currency: "CNY", Price: 100, Stock: -1, Raffle: &shop.RafflePlan{Mode: "instant", Prize: "礼物", Winners: 1, WinBPS: 10000, Campaign: "shared", TicketLimit: 6, UserLimit: 2, OrderLimit: 2}})
	}
	c, err = service.SaveCatalog(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	s.SetShopService(service)
	_, cookie, user := createAndLoginTestUser(t, manager, admin, "raffle-reader", "raffle-reader@example.test", "抽奖买家", "subscriber")
	headers := authenticatedAPIHeaders(user)
	create := func(key string, qty int, request string, status int, code string) {
		t.Helper()
		catalog, err := service.Catalog(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"product":%q,"quantity":%d,"version":"%d","request_id":%q,"accepted":true}`, key, qty, catalog.Version, request)
		w := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/shop/orders", body, headers)
		if w.Code != status || code != "" && !strings.Contains(w.Body.String(), code) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	create("a", 3, "too-many-per-order", 422, "shop.raffle_order_limit")
	create("a", 2, "first-order", 200, "")
	// Signed-in quotes without a coupon still enforce this account's allowance.
	w := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/shop/quote", `{"product":"a","quantity":1}`, headers)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "shop.raffle_user_limit") {
		t.Fatal("quote ignored authenticated buyer", w.Code, w.Body.String())
	}
	create("a", 1, "split-over-limit", 409, "shop.raffle_user_limit")
	create("b", 1, "remaining-campaign", 200, "")
	create("b", 1, "campaign-over-limit", 409, "shop.raffle_user_limit")
	w = serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/shop/session", "", nil)
	var session struct {
		Quotas map[string]shop.RaffleQuota `json:"raffle_quotas"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &session); err != nil || w.Code != 200 || session.Quotas["a"].UserProductLeft != 0 || session.Quotas["b"].UserCampaignLeft != 0 {
		t.Fatal("personal counts", w.Code, w.Body.String(), err)
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("private quota response is cacheable")
	}
	w = serveContentAPI(handler, nil, "GET", "https://example.test/api/v1/shop/catalog", "", nil)
	var overview struct {
		Quotas  map[string]shop.RaffleQuota `json:"raffle_quotas"`
		Catalog shop.Catalog                `json:"catalog"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &overview); err != nil || w.Code != 200 || overview.Quotas["a"].ProductLeft != 4 || overview.Quotas["a"].CampaignLeft != 7 || overview.Quotas["a"].UserProductLeft != 2 || overview.Quotas["a"].UserCampaignLeft != 3 || len(overview.Catalog.RaffleCampaigns) != 1 {
		t.Fatal("public counts leaked personal usage or missed campaign", w.Code, w.Body.String(), err)
	}
	w = serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/shop/quote", `{"product":"a","quantity":1}`, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"})
	if w.Code != 200 {
		t.Fatal("anonymous quote inherited previous buyer usage", w.Code, w.Body.String())
	}
}
