package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/plugin"
)

func TestAdminNavigationGroupsAndCapabilities(t *testing.T) {
	view := adminView{Current: "plugins", PluginSlug: "content-age", DetailTitle: "文章时效提醒", PersistentUsers: true,
		Principal:      auth.Principal{Capabilities: []string{auth.CapabilityAll}},
		PluginSettings: []plugin.SettingsPage{{Slug: "content-age", Title: "文章时效提醒", URL: "/admin/plugins/content-age/"}}}
	groups := view.Navigation()
	want := []string{"overview", "content", "interaction", "users", "commerce", "operations", "site", "plugins", "system"}
	if len(groups) != len(want) {
		t.Fatalf("groups = %#v", groups)
	}
	for i, group := range groups {
		if group.ID != want[i] {
			t.Fatalf("group %d = %s", i, group.ID)
		}
	}
	if !groups[7].Active || groups[7].Items[1].Active || !groups[7].Items[2].Active || view.Breadcrumb().Title != "扩展" || view.PageTitle() != "文章时效提醒" {
		t.Fatal("plugin detail navigation is not active")
	}
	view.Principal.Capabilities = []string{auth.CapabilityContentRead}
	groups = view.Navigation()
	if len(groups) != 1 || groups[0].ID != "content" || len(groups[0].Items) != 2 || groups[0].Items[0].ID != "content" || groups[0].Items[1].ID != "snippets" {
		t.Fatalf("capability leakage: %#v", groups)
	}
	view.ReadOnly = true
	if len(view.Navigation()) != 1 {
		t.Fatal("read-only expanded capabilities")
	}
	view.Principal.Capabilities = nil
	if len(view.Navigation()) != 0 {
		t.Fatal("empty groups visible")
	}
}

func TestAdminSharedLayoutRenderContract(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	progressionHTTPStore(t, s)
	for _, route := range []string{"/admin/", "/admin/content/", "/admin/content/new/", "/admin/appearance/", "/admin/settings/", "/admin/plugins/", "/admin/features/", "/admin/features/?edit=new", "/admin/features/links/?edit=new", "/admin/navigation/", "/admin/series/?edit=new", "/admin/seo/?edit=new", "/admin/wechat/", "/admin/currency/", "/admin/progression/", "/admin/tools/", "/admin/system/", "/admin/taxonomy/", "/admin/media/", "/admin/comments/", "/admin/sessions/", "/admin/analytics/", "/admin/analytics/ai/", "/admin/analytics/ai/requests/", "/admin/import/", "/admin/users/", "/admin/updates/", "/admin/plugins/updates/", "/admin/appearance/updates/", "/admin/series/", "/admin/seo/"} {
		t.Run(route, func(t *testing.T) {
			r := serveCookieRequest(handler, http.MethodGet, "https://example.test"+route, cookie, nil)
			body := r.Body.String()
			if r.Code != http.StatusOK {
				t.Fatalf("render status %d", r.Code)
			}
			entry := "chunnel-admin-workbench.js"
			if route == "/admin/content/new/" {
				entry = "newpress-editor-page.js"
			}
			for _, text := range []string{"data-admin-group=\"plugins\"", "chunnel-admin-skin.css", entry, "data-admin-command-input"} {
				if !strings.Contains(body, text) {
					t.Errorf("missing shared layout %s", text)
				}
			}
			if strings.Contains(body, "ZgotmplZ") {
				t.Fatal("invalid template URL")
			}
			assertAdminSavebarBindings(t, body)
			writeAdminLayoutSnapshot(t, route, body)
		})
	}
	t.Run("populated navigation", func(t *testing.T) {
		menu, err := s.navigation.Create(t.Context(), navigation.MenuInput{Name: "站点导航", Slug: "layout-check"}, owner.Username)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.navigation.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version, Actor: owner.Username}, []navigation.ItemInput{{ID: -1, Type: navigation.TypeCustom, Label: "首页", URL: "/"}})
		if err != nil {
			t.Fatal(err)
		}
		route := "/admin/navigation/?menu=" + strconv.FormatInt(menu.ID, 10)
		r := serveCookieRequest(handler, http.MethodGet, "https://example.test"+route, cookie, nil)
		body := r.Body.String()
		if r.Code != http.StatusOK || !strings.Contains(body, "admin-section-tabs") {
			t.Fatalf("populated navigation render status %d", r.Code)
		}
		assertAdminSavebarBindings(t, body)
		writeAdminLayoutSnapshot(t, route, body)
	})
}

func TestAdminPluginSettingsSharedRenderer(t *testing.T) {
	s, handler, _, cookie, p := newCommentHTTPTestServer(t)
	if err := s.plugins.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/admin/plugins/comment-origin/"} {
		r := serveCookieRequest(handler, http.MethodGet, "https://example.test"+route, cookie, nil)
		if r.Code != 200 || !strings.Contains(r.Body.String(), "data-admin-savebar") || !strings.Contains(r.Body.String(), "origin-provider") {
			t.Fatalf("builtin settings %s: %d", route, r.Code)
		}
		assertAdminSavebarBindings(t, r.Body.String())
		writeAdminLayoutSnapshot(t, route, r.Body.String())
	}
	retired := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/plugins/?settings=comment-origin", cookie, nil)
	if retired.Code != http.StatusBadRequest {
		t.Fatal("retired settings query accepted", retired.Code)
	}
	data := s.baseView("插件", "", "/admin/plugins/test/")
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-plugins"
	data.Admin = s.adminView("plugins", p, false)
	data.PluginRuntime = pluginRuntimeView{Manifest: plugin.Manifest{Slug: "test", Name: "<script>name</script>"}, Groups: []pluginSettingGroup{{ID: "general", Title: "常规", Fields: []pluginSettingView{{Setting: plugin.Setting{Key: "title", Label: "标题", Type: "text", Max: 80}, Value: `"><script>alert(1)</script>`}}}}}
	r := httptest.NewRecorder()
	s.render(r, 200, "admin-plugin-runtime", data)
	body := r.Body.String()
	if r.Code != 200 || strings.Contains(body, "<script>name") || strings.Contains(body, "<script>alert(1)") {
		t.Fatal("plugin values rendered unsafely")
	}
	ui, runtime := strings.Index(body, "js/chunnel-admin-workbench.js"), strings.Index(body, "js/chunnel-plugin-runtime.js")
	if ui < 0 || runtime <= ui || !strings.Contains(body, `data-form-id="plugin-runtime-form"`) {
		t.Fatal("plugin script dependency or save form binding missing")
	}
	assertAdminSavebarBindings(t, body)
	writeAdminLayoutSnapshot(t, "/admin/plugins/test/", body)
}

// Opt-in HTML snapshots let CSS audits use the real route markup without
// starting a server or reading an operator's site data.
func writeAdminLayoutSnapshot(t *testing.T, route, body string) {
	t.Helper()
	dir := os.Getenv("NEWPRESS_ADMIN_LAYOUT_SNAPSHOTS")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	name := strings.NewReplacer("/", "_", "?", "_", "=", "_", "&", "_").Replace(strings.Trim(route, "/"))
	if err := os.WriteFile(filepath.Join(dir, name+".html"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

// Parse the rendered document: source-level checks cannot detect forms discarded
// by the HTML parser, duplicate IDs, or a button bound to a non-form element.
func assertAdminSavebarBindings(t *testing.T, body string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	attr := func(n *html.Node, name string) (string, bool) {
		for _, a := range n.Attr {
			if a.Key == name {
				return a.Val, true
			}
		}
		return "", false
	}
	byID := map[string][]*html.Node{}
	var buttons []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if id, ok := attr(n, "id"); ok {
				byID[id] = append(byID[id], n)
			}
			if _, ok := attr(n, "data-admin-save"); ok {
				buttons = append(buttons, n)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	for _, button := range buttons {
		kind, _ := attr(button, "type")
		if kind == "button" { // Domain adapters submit several modules themselves.
			continue
		}
		id, _ := attr(button, "form")
		if kind != "submit" || len(byID[id]) != 1 || byID[id][0].Data != "form" {
			t.Errorf("shared save button has no unique form: %q", id)
		}
		for parent := button.Parent; parent != nil; parent = parent.Parent {
			if _, ok := attr(parent, "data-admin-savebar"); ok {
				owner, _ := attr(parent, "data-form-id")
				if owner != id {
					t.Errorf("save dock targets %q but its button targets %q", owner, id)
				}
				break
			}
		}
	}
}
