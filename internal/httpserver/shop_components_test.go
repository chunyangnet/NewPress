package httpserver

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/shop"
)

func TestShopComponentsEditorPersistenceVisibilityAndIsolation(t *testing.T) {
	s, h, manager, repo, cookie, admin := newPersistentAdminTestServer(t)
	b, err := shop.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetShopService(&shop.Service{Backend: b})
	image, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 4, 3)), media.UploadInput{UploadSource: "settings-image", Filename: "product.png"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	public := createWidgetPost(t, repo, content.PostInput{AuthorID: admin.UserID, Title: "商品配套文章", Slug: "shop-component-card", Status: content.StatusPublish, ContentSource: "说明正文"})
	c := testComponent("poll")
	video := testComponent("video")
	video.ID, video.Provider, video.UID = "product_video", "youtube", "dQw4w9WgXcQ"
	source := `<h2>详情标题</h2><script>window.shopAttack=true</script><img src="` + image.PublicURL + `" onerror="shopAttack()">` + componentSource(c) + componentSource(video) + fmt.Sprintf(`<aside data-np-widget="post" data-np-post="%d" data-np-layout="card"><p>旧引用快照</p></aside>`, public.ID)
	catalog := shop.InitialState().Catalog
	catalog.Enabled = true
	for _, key := range []string{"first", "second"} {
		catalog.Products = append(catalog.Products, shop.Product{Key: key, Title: "组件商品", Kind: "virtual", Mode: "direct", Enabled: true, Currency: "CNY", Price: 100, Stock: 10, DescriptionSource: source, DescriptionAuthorID: 99999})
	}
	save := func() {
		t.Helper()
		r := serveContentAPI(h, cookie, "PUT", "https://example.test/api/v1/admin/shop", currencyJSON(t, catalog), authenticatedAPIHeaders(admin))
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		var response struct {
			Catalog shop.Catalog `json:"catalog"`
		}
		decodeAPIResponse(t, r, &response)
		catalog = response.Catalog
	}
	save()
	if catalog.Products[0].DescriptionAuthorID != admin.UserID || catalog.Products[0].DescriptionSource != source {
		t.Fatal("source or owner lost")
	}
	catalog.Products[0].DescriptionAuthorID = 99999
	save()
	if catalog.Products[0].DescriptionAuthorID != admin.UserID {
		t.Fatal("forged component owner")
	}
	r := serveContentAPI(h, nil, "GET", "https://example.test/api/v1/shop/catalog", "", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), "description_source") || strings.Contains(r.Body.String(), "description_author_id") {
		t.Fatal("public source leak", r.Body.String())
	}
	r = serveContentAPI(h, nil, "GET", "https://example.test/shop/product/first/", "", nil)
	for _, want := range []string{`data-npw-post="shop:first"`, "详情标题", "商品配套文章", "newpress-components.js"} {
		if r.Code != 200 || !strings.Contains(r.Body.String(), want) {
			t.Fatal("missing product content", want, r.Code)
		}
	}
	for _, bad := range []string{"shopAttack", "旧引用快照"} {
		if strings.Contains(r.Body.String(), bad) {
			t.Fatal("unsafe/stale content", bad)
		}
	}
	if !strings.Contains(r.Header().Get("Content-Security-Policy"), "https://www.youtube-nocookie.com") {
		t.Fatal("missing frame policy")
	}
	url := "https://example.test/api/v1/article-components/shop:first/" + c.ID
	vote := currencyJSON(t, map[string]any{"action": "vote", "value": 1, "fingerprint": c.Fingerprint()})
	r = serveContentAPI(h, cookie, "POST", url, vote, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"})
	if r.Code != 403 {
		t.Fatal("missing CSRF accepted", r.Code)
	}
	r = serveContentAPI(h, cookie, "POST", url, vote, authenticatedAPIHeaders(admin))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	for key, total := range map[string]int{"first": 1, "second": 0} {
		r = serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/article-components/shop:"+key+"/"+c.ID, "", nil)
		var response struct {
			State articlewidgets.State `json:"state"`
		}
		decodeAPIResponse(t, r, &response)
		if r.Code != 200 || response.State.Total != total {
			t.Fatal("cross-product votes", key, response, r.Code)
		}
	}
	articleState, err := s.articleComponents.State(t.Context(), 1, admin.UserID, c, "")
	if err != nil || articleState.Total != 0 {
		t.Fatal("product votes leaked into article", articleState, err)
	}
	if _, err = repo.ChangeStatus(t.Context(), public.ID, content.Mutation{ExpectedVersion: public.Version}, content.StatusPrivate); err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(h, nil, "GET", "https://example.test/shop/product/first/", "", nil)
	if strings.Contains(r.Body.String(), "商品配套文章") || !strings.Contains(r.Body.String(), "文章暂不可用") {
		t.Fatal("private article reference leaked")
	}
	_, readerCookie, _ := createAndLoginTestUser(t, manager, admin, "shop-component-reader", "shop-components@example.test", "读者", "subscriber")
	validateURL := "https://example.test/api/v1/admin/shop/components/validate"
	for _, entry := range []struct {
		cookie *http.Cookie
		status int
	}{{nil, 401}, {readerCookie, 403}, {cookie, 200}} {
		r = serveContentAPI(h, entry.cookie, "POST", validateURL, currencyJSON(t, map[string]string{"config": c.Encode()}), authenticatedAPIHeaders(admin))
		if r.Code != entry.status {
			t.Fatal("validation authorization", r.Code, r.Body.String())
		}
	}
	r = serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/shop/article-widgets?page=1&q=商品", "", nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), "商品配套文章") {
		t.Fatal("picker private visibility", r.Code)
	}
	catalog.Products[0].Enabled = false
	save()
	r = serveContentAPI(h, cookie, "GET", url, "", nil)
	if r.Code != 404 {
		t.Fatal("unlisted component exposed", r.Code)
	}
	catalog.Products[0].Enabled = true
	catalog.Products[0].DescriptionSource = "<p>已移除组件</p>"
	save()
	r = serveContentAPI(h, cookie, "GET", url, "", nil)
	if r.Code != 404 {
		t.Fatal("removed component exposed", r.Code)
	}
	catalog.Enabled = false
	save()
	r = serveContentAPI(h, nil, "GET", "https://example.test/shop/product/second/", "", nil)
	if strings.Contains(r.Body.String(), `data-npw-post="shop:second"`) {
		t.Fatal("closed shop rendered interactive content")
	}
}
