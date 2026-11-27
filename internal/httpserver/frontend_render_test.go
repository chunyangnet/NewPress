package httpserver

import (
	"bytes"
	"compress/gzip"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
)

// Page-specific resources must preserve editing features while avoiding
// reader, editor and glass downloads on pages that do not use them.
func TestFrontendPageResourceAndLandmarkContract(t *testing.T) {
	t.Parallel()
	_, handler, _, repository, cookie, principal := newPersistentAdminTestServer(t)
	_, err := repository.Create(t.Context(), content.PostInput{
		Type: content.TypePost, Status: content.StatusPublish, Slug: "frontend-render-contract",
		Title: "Frontend render contract", ContentSource: "<p>Readable content.</p>",
		AuthorName: principal.Label(), Actor: principal.Username, CategoryIDs: []int64{1},
		CommentStatus: content.CommentOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Code blocks gain a component skin during rendering. That extra body class
	// must not remove the article's own reader and series styles.
	for _, kind := range []string{content.TypePost, content.TypePage} {
		_, err := repository.Create(t.Context(), content.PostInput{
			Type: kind, Status: content.StatusPublish, Slug: "frontend-component-" + kind,
			Title: "Component reading resources", ContentSource: "<h2>Chapter</h2><pre><code>read()</code></pre>",
			AuthorName: principal.Label(), Actor: principal.Username, CategoryIDs: []int64{1},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		path                 string
		authenticated        bool
		reader, emoji, glass bool
		interactions         bool
		page, component      bool
	}{
		{path: "/"},
		{path: "/explore/", emoji: true, interactions: true},
		{path: "/posts/frontend-render-contract", reader: true, emoji: true, interactions: true},
		{path: "/posts/frontend-component-post", reader: true, emoji: true, interactions: true, component: true},
		{path: "/pages/frontend-component-page", page: true, emoji: true, interactions: true, component: true},
		{path: "/admin/login/"},
		{path: "/admin/", authenticated: true},
		{path: "/admin/content/new/", authenticated: true, emoji: true, interactions: true},
		{path: "/admin/comments/", authenticated: true, emoji: true, interactions: true},
		{path: "/admin/appearance/", authenticated: true, glass: true},
		{path: "/admin/navigation/", authenticated: true},
	} {
		t.Run(test.path, func(t *testing.T) {
			var session *http.Cookie
			if test.authenticated {
				session = cookie
			}
			response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+test.path, session, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("render returned HTTP %d", response.Code)
			}
			body := response.Body.String()
			if test.component && !strings.Contains(body, "is-single ") {
				t.Fatal("component fixture did not render a reading page")
			}
			if test.component && !strings.Contains(body, "npw-skin-chunnel") {
				t.Fatal("component fixture must exercise the additional body class")
			}
			if test.reader && !strings.Contains(body, "chunnel-series.css") {
				t.Error("article must retain its series styles when components add a body class")
			}
			if !strings.Contains(body, "chunnel-color-mode.css") || !strings.Contains(body, "data-color-mode-control") {
				t.Error("page must expose shared appearance styles and controls")
			}
			if test.path != "/" && !strings.Contains(body, "chunnel-color-mode.js") {
				t.Error("page is missing the pre-paint appearance controller")
			}
			if test.path == "/" {
				if !strings.Contains(body, "chunnel-home.css") || !strings.Contains(body, "chunnel-home.js") {
					t.Error("home must load its compiled route assets")
				}
				for _, eager := range []string{"chunnel-home-features.js", "chunnel-home-search.js", "chunnel-ui.css", "chunnel-pages.css", "chunnel-experience.js", "chunnel-motion.js", "chunnel-foundation.css"} {
					if strings.Contains(body, eager) {
						t.Errorf("home eagerly loads optional resource %s", eager)
					}
				}
			}
			if strings.HasPrefix(test.path, "/admin/") {
				login := test.path == "/admin/login/"
				for resource, want := range map[string]bool{
					"chunnel-admin-base.css": true, "chunnel-admin-login.css": login,
					"chunnel-admin-login.js": login, "chunnel-admin.css": false, "chunnel-admin.js": !login,
					"chunnel-admin-shared.css": !login, "chunnel-admin-workflows.css": !login,
					"chunnel-admin-controls.css": true, "chunnel-admin-skin.css": !login,
					"chunnel-admin-login-skin.css": login,
					"chunnel-admin-editor.js":      test.path == "/admin/content/new/",
					"chunnel-admin-menu.js":        test.path == "/admin/navigation/",
				} {
					if strings.Contains(body, resource) != want {
						t.Errorf("admin resource %s present must be %t", resource, want)
					}
				}
			}
			for resource, want := range map[string]bool{
				"chunnel-admin-workspace.css":        strings.HasPrefix(test.path, "/admin/") && test.path != "/admin/appearance/",
				"chunnel-admin-layout-workspace.css": test.path == "/admin/appearance/",
				"chunnel-reader.css":                 test.reader || test.page, "chunnel-reader.js": test.reader,
				"chunnel-emoji-editor.js": test.emoji, "chunnel-emoji.js": test.emoji,
				"chunnel-glass.css": test.glass, "chunnel-interactions.css": test.interactions,
				"chunnel-foundation.css": test.path != "/" && !strings.HasPrefix(test.path, "/admin/"),
				"chunnel-explore.css":    test.path == "/explore/",
				"chunnel-motion.js":      test.path != "/" && !strings.HasPrefix(test.path, "/admin/"),
			} {
				if got := strings.Contains(body, resource); got != want {
					t.Errorf("resource %s present = %t, want %t", resource, got, want)
				}
			}
			if strings.Count(body, "<main ") != 1 || strings.Count(body, "</main>") != 1 {
				t.Error("page must expose exactly one main landmark")
			}
			nativeCSS := "chunnel-nojs.css"
			if test.path == "/" {
				nativeCSS = "chunnel-home-nojs.css"
			}
			if !regexp.MustCompile(`<noscript><link rel="stylesheet" href="/(?:theme-assets|core-assets)/[^"<>]*` + regexp.QuoteMeta(nativeCSS)).MatchString(body) {
				t.Error("native navigation fallback is missing")
			}
		})
	}
}

// Exercise the rendered asset graph, including navigation and appearance CSS.
// Match the deployment's gzip level 5 and 512-byte minimum, not source-file sizes.
func TestHomepageResourceBudget(t *testing.T) {
	t.Parallel()
	server, handler, _, _, _, _ := newPersistentAdminTestServer(t)
	noscript := regexp.MustCompile(`(?s)<noscript>.*?</noscript>`)
	links := regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)"`)
	scripts := regexp.MustCompile(`<script src="([^"]+)"`)
	for _, style := range []string{"classic", "liquid-glass"} {
		t.Run(style, func(t *testing.T) {
			values := server.themes.SettingsSnapshot().Values
			values["visual_style"] = style
			if _, fields, err := server.themes.SaveSettings(values); err != nil || len(fields) != 0 {
				t.Fatalf("set style: %v, %v", fields, err)
			}
			response := serveCookieRequest(handler, http.MethodGet, "https://example.test/", nil, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("home: HTTP %d", response.Code)
			}
			body := noscript.ReplaceAllString(response.Body.String(), "")
			entry := "chunnel-home.js"
			if style == "liquid-glass" {
				entry = "chunnel-home-glass.js"
			}
			if !strings.Contains(body, "/js/"+entry+"?v=") {
				t.Fatalf("%s must load %s", style, entry)
			}
			// Preserve the existing shell budgets. Consent is a separate optional
			// feature, with its own strict 2 KiB allowance per asset type.
			for _, group := range []struct {
				name    string
				pattern *regexp.Regexp
				limit   int
				native  bool
			}{
				{"CSS", links, 27 * 1024, false}, {"JS", scripts, 7 * 1024, false},
				{"CSS without JavaScript", links, 27 * 1024, true},
			} {
				total := 0
				consentTotal := 0
				resources := group.pattern.FindAllStringSubmatch(body, -1)
				if group.native {
					resources = group.pattern.FindAllStringSubmatch(response.Body.String(), -1)
				}
				if len(resources) == 0 {
					t.Fatalf("missing %s resources", group.name)
				}
				for _, match := range resources {
					path := html.UnescapeString(match[1])
					asset := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, nil, nil)
					if asset.Code != http.StatusOK {
						t.Fatalf("%s: HTTP %d", path, asset.Code)
					}
					value := asset.Body.Bytes()
					size := len(value)
					if size >= 512 {
						var compressed bytes.Buffer
						writer, err := gzip.NewWriterLevel(&compressed, 5)
						if err != nil {
							t.Fatal(err)
						}
						if _, err = writer.Write(value); err != nil {
							t.Fatal(err)
						}
						if err = writer.Close(); err != nil {
							t.Fatal(err)
						}
						size = compressed.Len()
					}
					if strings.Contains(path, "/css/cookie-consent.css?") || strings.Contains(path, "/js/cookie-consent.js?") {
						consentTotal += size
					} else {
						total += size
					}
				}
				t.Logf("%s %s: shell = %d B (< %d B), consent = %d B (< 2048 B), total = %d B", style, group.name, total, group.limit, consentTotal, total+consentTotal)
				if total >= group.limit {
					t.Errorf("%s exceeds homepage budget: %d >= %d", group.name, total, group.limit)
				}
				if consentTotal == 0 || consentTotal >= 2*1024 {
					t.Errorf("%s consent resources missing or exceed their 2 KiB budget: %d", group.name, consentTotal)
				}
			}
		})
	}
}
