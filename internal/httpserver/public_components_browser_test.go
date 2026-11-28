package httpserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/shop"
)

// Real Go-rendered public and admin pages. Browser writes and screenshot worker
// calls must be intercepted; the server denies any escaped business mutation.
func TestPublicComponentsBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_PUBLIC_COMPONENTS_BROWSER") != "1" {
		t.Skip("opt-in public components browser fixture")
	}
	dir := os.Getenv("NEWPRESS_PUBLIC_COMPONENTS_BROWSER_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		t.Fatal("browser evidence directory must be explicit and absolute")
	}
	s, handler, accounts, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
	reader, readerCookie, _ := createAndLoginTestUser(t, accounts, owner, "component-browser-reader", "component-browser-reader@example.test", "组件读者", "subscriber")
	ledger, _ := currencyHTTPStore(t, s, false)
	ownerBalance, err := ledger.Balance(t.Context(), owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	readerBalance, err := ledger.Balance(t.Context(), reader.ID)
	if err != nil {
		t.Fatal(err)
	}
	image := "/core-assets/images/favicon.svg"
	configs := []articlewidgets.Config{
		{Version: 1, ID: "browser_component_poll", Kind: "poll", Title: "下一次阅读", Mode: "both", Items: []articlewidgets.Item{{Title: "技术"}, {Title: "设计"}}},
		{Version: 1, ID: "browser_component_rating", Kind: "rating", Title: "文章评分", Mode: "both", Max: 5},
		{Version: 1, ID: "browser_component_redpacket", Kind: "redpacket", Title: "阅读奖励", Mode: "login", Amount: "1.00", Currency: "main", Count: 2},
		{Version: 1, ID: "browser_component_gift", Kind: "gift", Title: "阅读礼物", Caption: "测试礼物", Text: "写作愉快", URL: "/"},
		{Version: 1, ID: "browser_component_tabs", Kind: "tabs", Title: "内容分组", Items: []articlewidgets.Item{{Title: "简介", Text: "第一组正文"}, {Title: "细节", Text: "第二组正文"}}},
		{Version: 1, ID: "browser_component_chart", Kind: "chart", Title: "阅读图表", Layout: "bar", Max: 10, Items: []articlewidgets.Item{{Title: "技术", Value: 3}, {Title: "设计", Value: 7}}},
		{Version: 1, ID: "browser_component_compare", Kind: "compare", Title: "图片对比", Items: []articlewidgets.Item{{Title: "之前", Image: image}, {Title: "之后", Image: image}}},
		{Version: 1, ID: "browser_component_carousel", Kind: "carousel", Title: "图片轮播", Items: []articlewidgets.Item{{Title: "第一张", Image: image}, {Title: "第二张", Image: image}}},
		{Version: 1, ID: "browser_component_video", Kind: "video", Title: "视频", Provider: "bilibili", UID: "BV1234567890"},
		{Version: 1, ID: "browser_component_mention", Kind: "mention", Title: "作者资料", Provider: "site", UID: strconv.FormatInt(owner.UserID, 10)},
		{Version: 1, ID: "browser_component_code", Kind: "code", Title: "代码", Text: "const greeting: string = '你好';", Language: "typescript"},
	}
	var source strings.Builder
	for _, c := range configs {
		if err := c.Validate(); err != nil {
			t.Fatalf("%s: %v", c.Kind, err)
		}
		source.WriteString(componentSource(c))
	}
	post := createWidgetPost(t, repository, content.PostInput{Type: content.TypePost, AuthorID: owner.UserID, AuthorName: owner.DisplayName, Title: "公共组件验收", Slug: "public-components-browser", Status: content.StatusPublish, ContentSource: source.String()})
	shopBackend, err := shop.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetShopService(&shop.Service{Backend: shopBackend})
	catalog := shop.InitialState().Catalog
	catalog.Enabled = true
	catalog.Products = append(catalog.Products, shop.Product{Key: "component-product", Title: "组件商品", Kind: "virtual", Mode: "direct", Enabled: true, Currency: "CNY", Price: 100, Stock: 10, DescriptionSource: componentSource(configs[0]) + componentSource(configs[4]) + componentSource(configs[10]), DescriptionAuthorID: owner.UserID})
	if _, err := s.shop.SaveCatalog(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	shopBefore, err := shopBackend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := repository.ListRevisions(t.Context(), post.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	ownerBefore, err := accounts.GetUser(t.Context(), owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	readerBefore, err := accounts.GetUser(t.Context(), reader.ID)
	if err != nil {
		t.Fatal(err)
	}
	settingsBefore, _ := json.Marshal(s.siteSettings.Snapshot())
	states := func() map[string]articlewidgets.State {
		t.Helper()
		result := map[string]articlewidgets.State{}
		for _, c := range configs[:3] {
			for _, actor := range []string{"", fmt.Sprintf("u:%d", owner.UserID), fmt.Sprintf("u:%d", reader.ID)} {
				st, err := s.articleComponents.State(t.Context(), post.ID, post.AuthorID, c, actor)
				if err != nil {
					t.Fatal(err)
				}
				result[c.ID+":"+actor] = st
			}
		}
		for _, actor := range []string{"", fmt.Sprintf("u:%d", reader.ID)} {
			st, err := s.articleComponents.State(articlewidgets.WithScope(t.Context(), "shop:component-product"), 1, owner.UserID, configs[0], actor)
			if err != nil {
				t.Fatal(err)
			}
			result["shop:"+actor] = st
		}
		return result
	}
	statesBefore := states()
	var nonceBytes [16]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	login, readLogin, stop := "/__components_owner/"+nonce, "/__components_reader/"+nonce, "/__components_stop/"+nonce
	done, once := make(chan struct{}), sync.Once{}
	var escapedWrites, workerCalls atomic.Int64
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalls.Add(1)
		http.Error(w, "worker calls must be intercepted", http.StatusForbidden)
	}))
	t.Cleanup(worker.Close)
	s.config.DeviceMockupURL = worker.URL
	fixture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == login || r.URL.Path == readLogin) {
			cookie := ownerCookie
			if r.URL.Path == readLogin {
				cookie = readerCookie
			}
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: cookie.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == stop {
			cookie, err := r.Cookie(auth.CookieName)
			if err != nil {
				http.Error(w, "session required", 401)
				return
			}
			principal, ok := accounts.ValidateSessionContext(r.Context(), cookie.Value)
			if !ok || principal.UserID != owner.UserID {
				http.Error(w, "fixture owner required", 403)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			once.Do(func() { close(done) })
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			escapedWrites.Add(1)
			http.Error(w, "browser mutations must be intercepted", http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	})
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	s.config.PublicURL = server.URL
	writeEvidence := func(name string, value any) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		encodeErr := json.NewEncoder(file).Encode(value)
		closeErr := file.Close()
		if encodeErr != nil || closeErr != nil {
			t.Fatalf("write evidence: %v / %v", encodeErr, closeErr)
		}
	}
	writeEvidence("fixture.json", map[string]any{"base": server.URL, "login": login, "reader_login": readLogin, "stop": stop, "post_id": post.ID, "account_id": owner.UserID, "reader_id": reader.ID,
		"paths": map[string]string{"components": "/posts/" + post.Slug, "shop": "/shop/product/component-product/", "device": "/admin/tools/device-mockup/"}, "configs": configs, "states": statesBefore})
	t.Log("isolated public components fixture ready")
	select {
	case <-done:
	case <-time.After(25 * time.Minute):
		t.Fatal("fixture must be explicitly stopped within 25 minutes")
	case <-t.Context().Done():
		t.Fatal("fixture interrupted before explicit stop")
	}
	currentPost, err := repository.GetByID(t.Context(), post.ID)
	if err != nil || !reflect.DeepEqual(currentPost, post) {
		t.Fatal("article changed", err)
	}
	currentRevisions, err := repository.ListRevisions(t.Context(), post.ID, 20)
	if err != nil || !reflect.DeepEqual(currentRevisions, revisions) {
		t.Fatal("revisions changed", err)
	}
	currentOwner, err := accounts.GetUser(t.Context(), owner.UserID)
	if err != nil || !reflect.DeepEqual(currentOwner, ownerBefore) {
		t.Fatal("owner changed", err)
	}
	currentReader, err := accounts.GetUser(t.Context(), reader.ID)
	if err != nil || !reflect.DeepEqual(currentReader, readerBefore) {
		t.Fatal("reader changed", err)
	}
	settingsAfter, _ := json.Marshal(s.siteSettings.Snapshot())
	if !reflect.DeepEqual(settingsBefore, settingsAfter) || !reflect.DeepEqual(statesBefore, states()) {
		t.Fatal("settings or participation changed")
	}
	shopAfter, err := shopBackend.Read(t.Context())
	if err != nil || !reflect.DeepEqual(shopAfter, shopBefore) {
		t.Fatal("shop state changed", err)
	}
	currentOwnerBalance, err := ledger.Balance(t.Context(), owner.UserID)
	if err != nil || currentOwnerBalance != ownerBalance {
		t.Fatal("owner balance changed", err)
	}
	currentReaderBalance, err := ledger.Balance(t.Context(), reader.ID)
	if err != nil || currentReaderBalance != readerBalance {
		t.Fatal("reader balance changed", err)
	}
	if escapedWrites.Load() != 0 || workerCalls.Load() != 0 {
		t.Fatalf("unintercepted writes=%d worker calls=%d", escapedWrites.Load(), workerCalls.Load())
	}
	writeEvidence("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "content_unchanged": true, "revisions_unchanged": true, "balances_unchanged": true, "participation_unchanged": true, "settings_unchanged": true, "shop_unchanged": true, "unintercepted_writes": escapedWrites.Load(), "worker_calls": workerCalls.Load()})
}
