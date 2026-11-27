package shop

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestServiceModuleLibraryAndIconCoverage(t *testing.T) {
	library := ServiceModuleLibrary()
	if len(library.Templates) < 96 || len(library.Icons) < 40 || library.Limit != MaxServiceModules {
		t.Fatal("service library is incomplete")
	}
	data, err := os.ReadFile("../../third_party/lucide/icons.json")
	if err != nil {
		t.Fatal(err)
	}
	var sprite struct {
		Icons map[string]json.RawMessage `json:"icons"`
	}
	if err = json.Unmarshal(data, &sprite); err != nil {
		t.Fatal(err)
	}
	for _, icon := range library.Icons {
		if len(sprite.Icons[icon.Key]) == 0 {
			t.Fatalf("missing icon %s", icon.Key)
		}
	}
	seen := map[string]bool{}
	for _, preset := range library.Templates {
		if seen[preset.Template] || preset.Category == "" || !serviceModulesOK(&ServiceModules{Mode: "custom", Items: []ServiceModule{preset.ServiceModule}}) {
			t.Fatalf("invalid preset %#v", preset)
		}
		seen[preset.Template] = true
	}
	library.Templates[0].Title = "修改副本"
	library.Icons[0].Key = "changed"
	if ServiceModuleLibrary().Templates[0].Title == "修改副本" || ServiceModuleLibrary().Icons[0].Key == "changed" {
		t.Fatal("library shares mutable slices")
	}
}

func TestServiceModulesPersistAndPublicVisibility(t *testing.T) {
	p := item("service-modules", "virtual", "direct")
	custom := ServiceModule{Icon: "shield-check", Title: "授权咨询", Description: "下单前核对授权范围", Link: "#shop-product-rules"}
	p.Services = &ServiceModules{Mode: "custom", Items: []ServiceModule{ServiceModuleLibrary().Templates[0].ServiceModule, custom}}
	f := newFixture(t, p)
	b, err := NewFileBackend(f.path)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := (&Service{Backend: b}).Catalog(t.Context())
	if err != nil || catalog.Products[0].Services.Items[1] != custom {
		t.Fatal("modules did not persist", err)
	}
	public := PublicCatalog(catalog)
	public.Products[0].Services.Items[0].Title = "改动"
	if catalog.Products[0].Services.Items[0].Title == "改动" {
		t.Fatal("public catalog mutated original configuration")
	}
	for _, mode := range []string{"auto", "hidden"} {
		catalog.Products[0].Services.Mode = mode
		public = PublicCatalog(catalog)
		if public.Products[0].Services.Mode != mode || len(public.Products[0].Services.Items) != 0 || len(catalog.Products[0].Services.Items) != 2 {
			t.Fatalf("draft modules leaked or were lost in %s mode", mode)
		}
	}
	catalog.Products[0].Services = &ServiceModules{Mode: "custom"}
	if _, err = f.s.SaveCatalog(t.Context(), catalog); err != nil {
		t.Fatal("empty custom panel could not be saved", err)
	}
}

func TestServiceModulesRejectUnsafeOrInvalidConfiguration(t *testing.T) {
	p := item("invalid-services", "virtual", "direct")
	f := newFixture(t, p)
	valid := ServiceModule{Icon: "wallet", Title: "余额支付", Description: "核对付款金额", Link: "/shop/orders/"}
	cases := map[string]func(*ServiceModules){
		"mode":      func(c *ServiceModules) { c.Mode = "unknown" },
		"count":     func(c *ServiceModules) { c.Items = make([]ServiceModule, MaxServiceModules+1) },
		"title":     func(c *ServiceModules) { c.Items[0].Title = strings.Repeat("字", 25) },
		"blank":     func(c *ServiceModules) { c.Items[0].Title = " " },
		"markup":    func(c *ServiceModules) { c.Items[0].Description = "<script>alert(1)</script>" },
		"length":    func(c *ServiceModules) { c.Items[0].Description = strings.Repeat("字", 101) },
		"icon":      func(c *ServiceModules) { c.Items[0].Icon = "https://example.test/icon.svg" },
		"template":  func(c *ServiceModules) { c.Items[0].Template = "does-not-exist" },
		"duplicate": func(c *ServiceModules) { c.Items[0].Template = "currency"; c.Items = append(c.Items, c.Items[0]) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			catalog, _ := f.s.Catalog(t.Context())
			catalog.Products[0].Services = &ServiceModules{Mode: "custom", Items: []ServiceModule{valid}}
			change(catalog.Products[0].Services)
			if _, err := f.s.SaveCatalog(t.Context(), catalog); !errors.Is(err, ErrServiceModules) {
				t.Fatal("invalid service modules accepted", err)
			}
		})
	}
	for _, link := range []string{"javascript:alert(1)", "data:text/html,a", "//example.test", "/\\example.test", "/%5cexample.test", "/%2fexample.test", "/x%0ay", "http://example.test", "https://user:pass@example.test", " https://example.test"} {
		if serviceLinkOK(link) {
			t.Errorf("unsafe service link accepted: %q", link)
		}
	}
	for _, link := range []string{"", "/shop/orders/", "/shop/?coupons=1", "#shop-product-reviews", "https://example.test/help?a=1&b=2"} {
		if !serviceLinkOK(link) {
			t.Errorf("valid service link rejected: %q", link)
		}
	}
}

func TestServiceModuleEditsKeepExistingGroupsJoinable(t *testing.T) {
	p := item("service-group", "virtual", "group")
	p.Group = &GroupPlan{Size: 2, Price: 8000, Hours: 24}
	f := newFixture(t, p)
	first := f.create(t, p.Key, 1, nil)
	f.pay(t, first, 0)
	catalog, _ := f.s.Catalog(t.Context())
	catalog.Products[0].Services = &ServiceModules{Mode: "custom", Items: []ServiceModule{{Icon: "users", Title: "{成团人数} 人成团", Description: "通过团号邀请朋友"}}}
	if _, err := f.s.SaveCatalog(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	second := f.create(t, p.Key, 2, func(input *Purchase) { input.GroupID = first.GroupID })
	if second.GroupID != first.GroupID {
		t.Fatal("service presentation changed purchase terms")
	}
}
