package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"newpress.local/newpress/internal/layout"
)

func TestHomeCustomInstancesSavePreviewReloadAndRemove(t *testing.T) {
	s, handler, _, cookie, principal := newAuthenticatedContentServer(t)
	initial := s.themes.SettingsSnapshot()
	form := url.Values{"csrf_token": {principal.CSRFToken}, "revision": {initial.Revision}}
	for key, value := range initial.Values {
		form.Set(key, fmt.Sprint(value))
	}
	form.Set("home_custom_blocks", `[{"key":"custom_one","title":"横幅甲","html":"<p>独立内容甲</p><script>alert('unsafe-instance')</script>","surface":true,"visibility":"both"},{"key":"custom_two","title":"侧栏乙","html":"<p onclick=\"alert('unsafe-instance')\">独立内容乙</p>","surface":false,"visibility":"mobile"}]`)
	form.Set("home_top_order", "custom_one,hero")
	form.Set("home_aside_order", "custom_two")
	preview := serveContentForm(handler, cookie, "https://example.test/admin/appearance/preview/?page=home", "https://example.test", form)
	if preview.Code != 200 {
		t.Fatalf("preview %d: %s", preview.Code, preview.Body.String())
	}
	var document struct{ HTML string }
	if err := json.Unmarshal(preview.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`data-home-module="custom_one"`, `data-home-module="custom_two"`, "独立内容甲", "独立内容乙", `home-device-mobile`, `class="home-custom "`} {
		if !strings.Contains(document.HTML, fragment) {
			t.Errorf("missing %s", fragment)
		}
	}
	if strings.Contains(document.HTML, "unsafe-instance") || s.themes.SettingsSnapshot().Revision != initial.Revision {
		t.Fatal("unsafe HTML or persisted preview")
	}
	saved := serveContentForm(handler, cookie, "https://example.test/admin/appearance/layout/", "https://example.test", form)
	if saved.Code != 303 {
		t.Fatalf("save %d: %s", saved.Code, saved.Body.String())
	}
	if err := s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	var exported bytes.Buffer
	if err := s.themes.ExportSettings(&exported); err != nil {
		t.Fatal(err)
	}
	imported, fields, err := s.themes.ImportSettings(&exported)
	if err != nil || len(fields) != 0 || imported.Values["home_custom_blocks"] != form.Get("home_custom_blocks") {
		t.Fatalf("export/import lost instances: %v %v", err, fields)
	}
	for _, path := range []string{"/", "/admin/appearance/layout/", "/admin/appearance/"} {
		page := serveContentAPI(handler, cookie, "GET", "https://example.test"+path, "", nil)
		if page.Code != 200 || !strings.Contains(page.Body.String(), "独立内容甲") || !strings.Contains(page.Body.String(), "独立内容乙") {
			t.Fatalf("reload %s: %d", path, page.Code)
		}
		if strings.Contains(page.Body.String(), `id="settings-home_custom"`) || strings.Contains(page.Body.String(), `id="layout-home_custom"`) {
			t.Fatal("standalone HTML settings group remains")
		}
		if path != "/" && strings.Count(page.Body.String(), `name="home_custom_blocks"`) != 1 {
			t.Fatal("instance payload missing or duplicated")
		}
		if path == "/admin/appearance/" && strings.Count(page.Body.String(), "data-home-editor=") != 3 {
			t.Fatal("appearance must retain the three list editors")
		}
	}
	form.Set("revision", s.themes.SettingsSnapshot().Revision)
	form.Set("home_custom_blocks", "[]")
	form.Set("home_top_order", "hero")
	form.Set("home_aside_order", "")
	removed := serveContentForm(handler, cookie, "https://example.test/admin/appearance/layout/", "https://example.test", form)
	if removed.Code != 303 {
		t.Fatalf("remove %d: %s", removed.Code, removed.Body.String())
	}
	public := serveContentAPI(handler, nil, "GET", "https://example.test/", "", nil)
	if strings.Contains(public.Body.String(), "独立内容") || strings.Contains(public.Body.String(), `data-home-module="custom`) {
		t.Fatal("removed custom content still rendered")
	}
}

func TestHomeCustomInstancesAppearancePreservesLayout(t *testing.T) {
	s, handler, _, cookie, _ := newAuthenticatedContentServer(t)
	initial := s.themes.SettingsSnapshot().Values
	for _, scenario := range []struct{ name, payload, order string }{
		{"empty", "[]", "hero"},
		{"instances", `[{"key":"custom_one","title":"保留甲","html":"<p>内容甲</p>","surface":false,"visibility":"mobile"},{"key":"custom_two","title":"保留乙","html":"<p>内容乙</p>","surface":true,"visibility":"desktop"}]`, "custom_one,hero,custom_two"},
		{"removed", "[]", "hero"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			values := maps.Clone(initial)
			values["home_custom_blocks"], values["home_top_order"] = scenario.payload, scenario.order
			values["home_recent_visibility"], values["home_carousel_visibility"] = "desktop", "mobile"
			values["home_hero_visibility"] = "hidden"
			before, fields, err := s.themes.SaveSettings(values)
			if err != nil || len(fields) != 0 {
				t.Fatalf("prepare: %v %v", err, fields)
			}
			page := serveContentAPI(handler, cookie, "GET", "https://example.test/admin/appearance/", "", nil)
			if page.Code != 200 {
				t.Fatalf("appearance: %d", page.Code)
			}
			body := page.Body.String()
			for _, old := range []string{`id="settings-home_custom"`, `id="setting-home_custom_html"`} {
				if strings.Contains(body, old) {
					t.Errorf("old appearance control remains: %s", old)
				}
			}
			if !strings.Contains(body, `href="/admin/appearance/layout/"`) || !strings.Contains(body, `>首页编排</`) || strings.Count(body, "data-home-editor=") != 3 || strings.Count(body, "data-home-custom-add") != 3 {
				t.Fatal("appearance lacks list editors, HTML controls or canvas link")
			}
			// Both editing surfaces normalize older incomplete orders, visibility
			// switches and singleton content to the shared instance representation.
			for _, region := range layout.Resolve(before.Values) {
				before.Values["home_"+region.Key+"_order"] = region.Order
				for _, block := range region.Modules {
					if block.Custom == nil {
						before.Values["home_"+block.Key+"_visibility"] = block.Visibility
					}
				}
			}
			before.Values["home_custom_blocks"] = layout.EditorCustomJSON(before.Values)
			form := renderedAppearanceForm(t, body)
			parsed, fields := s.themes.ParseSettingsForm(form)
			if len(fields) != 0 || !reflect.DeepEqual(parsed, before.Values) {
				t.Fatalf("rendered form loses settings: %v", fields)
			}
			form.Set("accent", "#123456")
			saved := serveContentForm(handler, cookie, "https://example.test/admin/appearance/", "https://example.test", form)
			if saved.Code != 303 {
				t.Fatalf("appearance save: %d: %s", saved.Code, saved.Body.String())
			}
			before.Values["accent"] = "#123456"
			if !reflect.DeepEqual(before.Values, s.themes.SettingsSnapshot().Values) {
				t.Fatal("appearance save changed layout or HTML content")
			}
			canvas := serveContentAPI(handler, cookie, "GET", "https://example.test/admin/appearance/layout/", "", nil)
			if canvas.Code != 200 || strings.Contains(canvas.Body.String(), `data-home-key="custom"`) || strings.Contains(canvas.Body.String(), `id="layout-home_custom"`) {
				t.Fatal("empty legacy card or separate HTML submenu remains on canvas")
			}
			if scenario.name == "instances" {
				form.Set("revision", s.themes.SettingsSnapshot().Revision)
				form.Set("home_custom_blocks", `[{"key":"custom_one","title":"列表编辑甲","html":"<p>列表更新内容</p>","surface":false,"visibility":"both"},{"key":"custom_two","title":"保留乙","html":"<p>内容乙</p>","surface":true,"visibility":"mobile"}]`)
				form.Set("home_top_order", strings.ReplaceAll(form.Get("home_top_order"), ",custom_two", ""))
				form.Set("home_aside_order", "custom_two,"+form.Get("home_aside_order"))
				updated := serveContentForm(handler, cookie, "https://example.test/admin/appearance/", "https://example.test", form)
				if updated.Code != 303 {
					t.Fatalf("list edit: %d: %s", updated.Code, updated.Body.String())
				}
				canvas = serveContentAPI(handler, cookie, "GET", "https://example.test/admin/appearance/layout/", "", nil)
				canvasForm := renderedAppearanceForm(t, canvas.Body.String())
				if canvasForm.Get("home_aside_order") != form.Get("home_aside_order") || !strings.Contains(canvasForm.Get("home_custom_blocks"), "列表更新内容") {
					t.Fatal("list edits did not reach the visual canvas")
				}
			}
		})
	}
}

// Read successful controls from the actual rendered form, including unchecked
// booleans and hidden layout values, so an omitted field cannot pass the test.
func renderedAppearanceForm(t *testing.T, body string) url.Values {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	form, counts := url.Values{}, map[string]int{}
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, inside bool) {
		attrs := map[string]string{}
		for _, attr := range node.Attr {
			attrs[attr.Key] = attr.Val
		}
		if node.Type == html.ElementNode && node.Data == "form" {
			inside = attrs["id"] == "theme-settings-form"
		}
		if inside && attrs["name"] != "" {
			name := attrs["name"]
			if attrs["type"] != "radio" {
				counts[name]++
				if counts[name] > 1 {
					t.Errorf("duplicate form control: %s", name)
				}
			}
			var textContent func(*html.Node) string
			textContent = func(n *html.Node) string {
				if n.Type == html.TextNode {
					return n.Data
				}
				var text string
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					text += textContent(child)
				}
				return text
			}
			switch node.Data {
			case "input":
				_, checked := attrs["checked"]
				if attrs["type"] != "checkbox" && attrs["type"] != "radio" || checked {
					form.Add(name, attrs["value"])
				}
			case "textarea":
				form.Add(name, textContent(node))
			case "select":
				for option := node.FirstChild; option != nil; option = option.NextSibling {
					optionAttrs := map[string]string{}
					for _, attr := range option.Attr {
						optionAttrs[attr.Key] = attr.Val
					}
					if _, selected := optionAttrs["selected"]; selected {
						form.Add(name, optionAttrs["value"])
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, inside)
		}
	}
	walk(doc, false)
	for name, values := range form {
		if len(values) != 1 {
			t.Errorf("ambiguous form value: %s", name)
		}
	}
	return form
}
