package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/presentation"
	"newpress.local/newpress/internal/theme"
)

func translationTestServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "starter")
	if err := themes.LoadExplicit("starter"); err != nil {
		t.Fatal(err)
	}
	s := New(config.Config{SiteTitle: "Translations", PublicURL: "https://example.test", ActiveTheme: "starter"}, content.NewMemoryRepository(false), themes, plugin.NewManager(t.TempDir()))
	return s, s.routes()
}

func TestTranslationsAPIAndSiteLocale(t *testing.T) {
	s, handler := translationTestServer(t)
	const endpoint = "https://example.test/api/v1/translations"
	get := func(method, query, etag string) *httptest.ResponseRecorder {
		return serveContentAPI(handler, nil, method, endpoint+query, "", map[string]string{"If-None-Match": etag})
	}
	first := get("GET", "", "")
	var response translationsAPIResponse
	decodeAPIResponse(t, first, &response)
	if first.Code != 200 || response.Locale != "zh-CN" || response.Theme != "starter" || response.Messages["starter.latest"] != "最新文章" || response.Messages["navigation.home"] != "首页" {
		t.Fatal(first.Code, response)
	}
	if !slices.Equal(response.Locales, []string{"en", "ja", "zh-CN", "zh-Hant"}) {
		t.Fatal("missing bundled languages", response.Locales)
	}
	if first.Header().Get("Cache-Control") != "public, no-cache" || first.Header().Get("ETag") == "" {
		t.Fatal(first.Header())
	}
	english := get("GET", "?locale=EN-us", "")
	decodeAPIResponse(t, english, &response)
	if response.Locale != "en-US" || response.Messages["starter.latest"] != "Latest posts" || response.Messages["navigation.home"] != "Home" {
		t.Fatal(response)
	}
	if english.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Fatal("locale did not change ETag")
	}
	for _, validator := range []string{english.Header().Get("ETag"), "W/" + english.Header().Get("ETag"), "*"} {
		cached := get("GET", "?locale=en-US", validator)
		if cached.Code != 304 || cached.Body.Len() != 0 {
			t.Fatal(cached.Code, cached.Body.String())
		}
	}
	head := get("HEAD", "?locale=en-US", "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("ETag") != english.Header().Get("ETag") || head.Header().Get("Content-Length") != english.Header().Get("Content-Length") {
		t.Fatal(head.Code, head.Header(), head.Body.String())
	}
	single := get("GET", "?locale=en-GB&key=comments.title", "")
	var one translationsAPIResponse
	decodeAPIResponse(t, single, &one)
	if len(one.Messages) != 1 || one.Messages["comments.title"] != "Comments ({count})" {
		t.Fatal(one)
	}
	unknownLocale := get("GET", "?locale=de&key=starter.latest", "")
	var fallback translationsAPIResponse
	decodeAPIResponse(t, unknownLocale, &fallback)
	if fallback.Locale != "de" || fallback.Messages["starter.latest"] != "最新文章" {
		t.Fatal(fallback)
	}

	previous := first.Header().Get("ETag")
	for _, test := range []struct{ locale, home, latest, comments string }{
		{"en-US", "Home", "Latest posts", "Comments ({count})"},
		{"zh-Hant", "首頁", "最新文章", "留言（{count}）"},
		{"zh-TW", "首頁", "最新文章", "留言（{count}）"},
		{"zh-HK", "首頁", "最新文章", "留言（{count}）"},
		{"ja", "ホーム", "最新の記事", "コメント（{count}件）"},
		{"ja-JP", "ホーム", "最新の記事", "コメント（{count}件）"},
		{"zh-CN", "首页", "最新文章", "评论（{count}）"},
	} {
		t.Run(test.locale, func(t *testing.T) {
			queried := get("GET", "?locale="+test.locale+"&key=comments.title", "")
			var single translationsAPIResponse
			decodeAPIResponse(t, queried, &single)
			if queried.Code != 200 || single.Locale != test.locale || len(single.Messages) != 1 || single.Messages["comments.title"] != test.comments {
				t.Fatal(queried.Code, single)
			}
			initial := s.siteSettings.Snapshot()
			settings := initial.Settings
			settings.Identity.Locale = test.locale
			if _, fields, err := s.siteSettings.Save(initial.Revision, settings); err != nil || len(fields) > 0 {
				t.Fatal(fields, err)
			}
			changed := get("GET", "", previous)
			var translated translationsAPIResponse
			decodeAPIResponse(t, changed, &translated)
			if changed.Code != 200 || translated.Locale != test.locale || translated.Messages["navigation.home"] != test.home || translated.Messages["starter.latest"] != test.latest {
				t.Fatal("site locale did not change translations", changed.Code, translated)
			}
			if changed.Header().Get("ETag") == previous {
				t.Fatal("site locale did not change ETag")
			}
			previous = changed.Header().Get("ETag")
			home := serveContentAPI(handler, nil, "GET", "https://example.test/", "", nil)
			if home.Code != 200 || !strings.Contains(home.Body.String(), test.latest) || !strings.Contains(home.Body.String(), `lang="`+test.locale+`"`) {
				t.Fatal(home.Code, home.Body.String())
			}
		})
	}
}

func TestTranslationsAPIRejectsInvalidInputAndHonorsMaintenance(t *testing.T) {
	s, handler := translationTestServer(t)
	const endpoint = "https://example.test/api/v1/translations"
	for _, query := range []string{"?locale=", "?locale=../en", "?locale=en_US", "?locale=%ff", "?locale=en&locale=zh-CN", "?locale=%ZZ", "?key=", "?key=../file", "?key=%ff", "?key=a&key=b", "?unknown=value"} {
		response := serveContentAPI(handler, nil, "GET", endpoint+query, "", nil)
		assertAPIError(t, response, 400, "request.invalid_query")
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(response.Header())
		}
	}
	assertAPIError(t, serveContentAPI(handler, nil, "GET", endpoint+"?key=not.found", "", nil), 404, "translations.not_found")
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		response := serveContentAPI(handler, nil, method, endpoint, "", nil)
		assertAPIError(t, response, 405, "request.method_not_allowed")
		if response.Header().Get("Allow") != "GET, HEAD" {
			t.Fatal(response.Header())
		}
	}
	initial := s.siteSettings.Snapshot()
	settings := initial.Settings
	settings.Operations.MaintenanceEnabled = true
	if _, fields, err := s.siteSettings.Save(initial.Revision, settings); err != nil || len(fields) > 0 {
		t.Fatal(fields, err)
	}
	response := serveContentAPI(handler, nil, "GET", endpoint, "", nil)
	assertAPIError(t, response, 503, "site.maintenance")
}

func TestLocaleProvidersMatchBothThemeContracts(t *testing.T) {
	page := pageView{Site: presentation.Site{Locale: "en-GB"}}
	for _, provider := range []theme.LocaleProvider{page, chunnelPage(page), publicPage(page)} {
		if provider.SiteLocale() != "en-GB" {
			t.Fatal("adapter lost site locale")
		}
	}
}

func TestTranslationsAPIChangesETagAfterThemeReload(t *testing.T) {
	themes := boundaryTheme(t, "translated-api", "body{}", `{{t "custom.label"}}`)
	s := New(config.Config{SiteTitle: "Translations", PublicURL: "https://example.test"}, content.NewMemoryRepository(false), themes, plugin.NewManager(t.TempDir()))
	handler := s.routes()
	directory := filepath.Join(filepath.Dir(themes.AssetDir()), "locales")
	if err := os.Mkdir(directory, 0750); err != nil {
		t.Fatal(err)
	}
	var previous string
	for _, label := range []string{"First", "Second"} {
		body := `{"schema":1,"locale":"zh-CN","messages":{"custom.label":"` + label + `"}}`
		if err := os.WriteFile(filepath.Join(directory, "zh-CN.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := themes.Load(); err != nil {
			t.Fatal(err)
		}
		response := serveContentAPI(handler, nil, "GET", "https://example.test/api/v1/translations?key=custom.label", "", map[string]string{"If-None-Match": previous})
		var data translationsAPIResponse
		decodeAPIResponse(t, response, &data)
		if response.Code != 200 || data.Messages["custom.label"] != label || response.Header().Get("ETag") == previous {
			t.Fatal(response.Code, data, response.Header())
		}
		previous = response.Header().Get("ETag")
	}
}
