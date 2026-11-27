package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestOAuthAccountBindingSessionAndLogin(t *testing.T) {
	s, h, m, _, _, owner := newPersistentAdminTestServer(t)
	u, cookie, p := createAndLoginTestUser(t, m, owner, "binding-reader", "binding@example.test", "Binding", "subscriber")
	configureTestLoginProvider(t, s, "github", "binding-client", "binding-secret")
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": p.CSRFToken}
	base := "https://example.test/api/v1/social/oauth-bindings/github"
	if w := serveContentAPI(h, cookie, "POST", base+"/start", "{}", nil); w.Code != 403 {
		t.Fatal("missing CSRF", w.Code)
	}
	calls := 0
	s.registration.client = &http.Client{Transport: oauthTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() == oauthEndpoints["github"].Token {
			return oauthTestResponse(`{"access_token":"fixture"}`), nil
		}
		return oauthTestResponse(`{"id":1234,"name":"Bound reader"}`), nil
	})}
	start := func() (string, *http.Cookie) {
		w := serveContentAPI(h, cookie, "POST", base+"/start", "{}", headers)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		redirect, _ := url.Parse(result.URL)
		var browser *http.Cookie
		for _, c := range w.Result().Cookies() {
			if c.Name == accessBrowserCookie {
				browser = c
			}
		}
		if browser == nil {
			t.Fatal("missing browser cookie")
		}
		return "https://example.test/auth/github/callback?code=fixture&state=" + url.QueryEscape(redirect.Query().Get("state")), browser
	}
	callback, browser := start()
	bad := serveContentAPI(h, browser, "GET", callback, "", nil)
	if bad.Code != 303 || calls != 0 {
		t.Fatal("binding without original session", bad.Code, calls)
	}
	callback, browser = start()
	r := httptest.NewRequest("GET", callback, nil)
	r.AddCookie(cookie)
	r.AddCookie(browser)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Location") != "/account/?view=security#binding_success" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	fresh, err := m.GetUser(t.Context(), u.ID)
	if err != nil || fresh.OAuthBindings["github"] == "" {
		t.Fatal("missing binding", err)
	}
	found, err := m.FindRegistration(t.Context(), "external", fresh.OAuthBindings["github"])
	if err != nil || found.ID != u.ID {
		t.Fatal("login lookup", err)
	}
	before := calls
	h.ServeHTTP(httptest.NewRecorder(), r)
	if calls != before {
		t.Fatal("state replay")
	}
	removed := serveContentAPI(h, cookie, "POST", base+"/remove", fmt.Sprintf(`{"version":%d}`, fresh.Version), headers)
	if removed.Code != 200 {
		t.Fatal(removed.Code, removed.Body.String())
	}
}
