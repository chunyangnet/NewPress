package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/linkpage"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/mediausage"
	"newpress.local/newpress/internal/shop"
)

func TestSettingsShareMediaPickerAndImageControls(t *testing.T) {
	_, h, token, _, _ := newMediaHTTPTestServer(t, 4096)
	for _, page := range []struct{ path, field, script string }{
		{"/admin/settings/", "site_icon_url", "chunnel-admin-media.js"}, {"/admin/appearance/", "home_slide_1_image", "chunnel-admin-media.js"},
		{"/admin/series/?edit=new", "cover_url", "chunnel-admin-media.js"}, {"/admin/content/new/", "", "newpress-editor-page.js"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, authenticatedMediaRequest("GET", "https://example.test"+page.path, nil, token))
		body := w.Body.String()
		if w.Code != 200 || strings.Count(body, "data-editor-media-dialog ") != 1 || strings.Count(body, "js/"+page.script) != 1 || strings.Contains(body, "js/chunnel-editor-media.js") || strings.Contains(body, "js/chunnel-media-fields.js") {
			t.Fatalf("%s: status %d or missing/duplicated shared picker", page.path, w.Code)
		}
		lazy := page.script == "newpress-editor-page.js"
		if strings.Contains(body, "data-media-script=") != lazy || strings.Contains(body, "js/chunnel-admin-media.js") == lazy || strings.Contains(body, "js/newpress-editor-media-loader.js") {
			t.Fatalf("%s: media loading mode is not isolated to its page", page.path)
		}
		if page.field != "" && (!strings.Contains(body, `name="`+page.field+`"`) || !strings.Contains(body, "data-media-input")) {
			t.Fatalf("%s: missing image control", page.path)
		}
	}
}

func TestSettingsMediaReferencesProtectLinksProductsAndBadges(t *testing.T) {
	s, h, _, _, cookie, principal := newPersistentAdminTestServer(t)
	item, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 16, 12)), media.UploadInput{Filename: "图标 (测试).png", UploadSource: "settings-image"}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	settings := linkpage.Defaults()
	if _, err = s.linkPages.Apply(t.Context(), "friends", linkpage.Actor{ID: principal.UserID, Admin: true}, linkpage.Command{Action: "create", Settings: &settings, Key: "create-media-links", ExpectedUserID: principal.UserID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	command := linkpage.Command{Action: "save_item", Version: 1, Key: "save-media-link", ExpectedUserID: principal.UserID, Item: &linkpage.Item{Name: "Media friend", URL: "https://example.com", LogoURL: item.PublicURL, Enabled: true}}
	raw, _ := json.Marshal(command)
	w := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/link-pages/friends", string(raw), authenticatedAPIHeaders(principal))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	board, _ := s.linkPages.Get(t.Context(), "friends")
	if got := linkView(board, board.Items[0]).Icon; got != item.PublicURL {
		t.Fatal(got)
	}
	backend, _ := shop.NewFileBackend("")
	s.SetShopService(&shop.Service{Backend: backend})
	catalog := shop.InitialState().Catalog
	catalog.Products = []shop.Product{{Key: "media-product", Title: "Media product", Kind: "virtual", Mode: "direct", Currency: "CNY", Price: 100, Stock: -1, Images: []string{item.PublicURL}}}
	if _, err = s.shop.SaveCatalog(t.Context(), catalog); err != nil {
		t.Fatal(err)
	}
	service, _, _ := recognitionHTTPStore(t, s)
	state, _ := service.Backend.Read(t.Context())
	state.Settings.Badges[0].Image = item.PublicURL
	if err = service.SaveSettings(t.Context(), state.Settings); err != nil {
		t.Fatal(err)
	}
	report, err := s.mediaUsage.Report(t.Context(), item, mediausage.Access{Settings: true}, 0)
	if err != nil || report.Total != 3 {
		t.Fatalf("%+v: %v", report, err)
	}
	for _, source := range []string{mediausage.SourceLink, mediausage.SourceShop, mediausage.SourceBadge} {
		if report.Counts[source] != 1 {
			t.Fatal(report.Counts)
		}
	}
	if err = s.mediaUsage.Trash(t.Context(), item.ID, item.Version); !errors.Is(err, mediausage.ErrInUse) {
		t.Fatal(err)
	}
	command.Version = 2
	command.Key = "reject-missing-media"
	command.Item.LogoURL = "/media/999/missing.png"
	raw, _ = json.Marshal(command)
	w = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/link-pages/friends", string(raw), authenticatedAPIHeaders(principal))
	if w.Code != 422 || !strings.Contains(w.Body.String(), "media.invalid_reference") {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Opt-in browser harness uses disposable stores and an authenticated local server.
func TestSettingsMediaBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_MEDIA_PICKER_FIXTURE")
	if dir == "" {
		t.Skip("set NEWPRESS_MEDIA_PICKER_FIXTURE for browser verification")
	}
	s, h, _, _, cookie, principal := newPersistentAdminTestServer(t)
	commerceHTTPStore(t, s)
	recognitionHTTPStore(t, s)
	backend, _ := shop.NewFileBackend("")
	s.SetShopService(&shop.Service{Backend: backend})
	settings := linkpage.Defaults()
	if _, err := s.linkPages.Apply(t.Context(), "friends", linkpage.Actor{ID: principal.UserID, Admin: true}, linkpage.Command{Action: "create", Settings: &settings, Key: "create-browser-links", ExpectedUserID: principal.UserID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"清晨山间.png", "品牌图标.png", "分享封面.png"} {
		if _, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 320+i*30, 200+i*20)), media.UploadInput{Filename: name, UploadSource: "media-library", AltText: "媒体库测试图片"}, 1<<20); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{}, 1)
	mux := http.NewServeMux()
	cookie.Secure = false
	mux.HandleFunc("GET /fixture/admin", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/settings/#site-settings-identity", 303)
	})
	mux.HandleFunc("POST /fixture/stop", func(w http.ResponseWriter, r *http.Request) {
		select {
		case done <- struct{}{}:
		default:
		}
		w.WriteHeader(204)
	})
	mux.Handle("/", h)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"url": host.URL})
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("media picker browser fixture ready", host.URL)
	select {
	case <-done:
	case <-time.After(15 * time.Minute):
	case <-t.Context().Done():
	}
}
