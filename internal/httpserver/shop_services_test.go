package httpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"newpress.local/newpress/internal/shop"
)

func TestShopServiceModulesAdminAndPublicAPI(t *testing.T) {
	s, handler, _, _, cookie, admin := newPersistentAdminTestServer(t)
	backend, err := shop.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetShopService(&shop.Service{Backend: backend})
	catalog := shop.InitialState().Catalog
	catalog.Enabled = true
	catalog.Products = []shop.Product{{Key: "services", Title: "服务模块商品", Kind: "virtual", Mode: "direct", Enabled: true, Currency: "CNY", Price: 100, Stock: 10, Services: &shop.ServiceModules{Mode: "custom", Items: []shop.ServiceModule{{Template: "currency", Icon: "wallet", Title: "{结算货币}结算", Description: "下单前核对付款金额"}, {Icon: "library", Title: "查看说明", Description: "使用前阅读", Link: "#shop-product-description"}}}}}
	response := serveContentAPI(handler, cookie, "PUT", "https://example.test/api/v1/admin/shop", currencyJSON(t, catalog), authenticatedAPIHeaders(admin))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var saved struct {
		Catalog shop.Catalog `json:"catalog"`
	}
	decodeAPIResponse(t, response, &saved)
	catalog = saved.Catalog
	if len(catalog.Products[0].Services.Items) != 2 {
		t.Fatal("service items lost on save")
	}
	response = serveContentAPI(handler, cookie, "GET", "https://example.test/api/v1/admin/shop", "", nil)
	var editor struct {
		Library shop.ServiceLibrary `json:"service_library"`
	}
	decodeAPIResponse(t, response, &editor)
	if response.Code != 200 || len(editor.Library.Templates) != 96 || editor.Library.Limit != 60 {
		t.Fatal("missing editor templates", response.Code)
	}
	response = serveContentAPI(handler, nil, "GET", "https://example.test/api/v1/shop/catalog", "", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "查看说明") || strings.Contains(response.Body.String(), "service_library") {
		t.Fatal("incorrect public service data", response.Code, response.Body.String())
	}
	response = serveContentAPI(handler, nil, "GET", "https://example.test/shop/product/services/", "", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "newpress-shop-services.js") {
		t.Fatal("shared renderer not loaded", response.Code)
	}
	catalog.Products[0].Services.Items[0].Link = "javascript:alert(1)"
	response = serveContentAPI(handler, cookie, "PUT", "https://example.test/api/v1/admin/shop", currencyJSON(t, catalog), authenticatedAPIHeaders(admin))
	if response.Code != 422 || !strings.Contains(response.Body.String(), "shop.service_modules_invalid") {
		t.Fatal("unsafe module accepted", response.Code, response.Body.String())
	}
	catalog.Products[0].Services.Items[0].Link = ""
	catalog.Products[0].Services.Mode = "hidden"
	response = serveContentAPI(handler, cookie, "PUT", "https://example.test/api/v1/admin/shop", currencyJSON(t, catalog), authenticatedAPIHeaders(admin))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	decodeAPIResponse(t, response, &saved)
	catalog = saved.Catalog
	response = serveContentAPI(handler, nil, "GET", "https://example.test/api/v1/shop/catalog", "", nil)
	if strings.Contains(response.Body.String(), "查看说明") || !strings.Contains(response.Body.String(), `"mode":"hidden"`) {
		t.Fatal("hidden configuration leaked", response.Body.String())
	}
	var reset map[string]any
	if err = json.Unmarshal([]byte(currencyJSON(t, catalog)), &reset); err != nil {
		t.Fatal(err)
	}
	reset["products"].([]any)[0].(map[string]any)["services"] = nil
	response = serveContentAPI(handler, cookie, "PUT", "https://example.test/api/v1/admin/shop", currencyJSON(t, reset), authenticatedAPIHeaders(admin))
	if response.Code != 200 {
		t.Fatal("legacy automatic reset failed", response.Code, response.Body.String())
	}
}
