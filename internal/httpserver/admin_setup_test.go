package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/config"
)

func TestStrictSetupOrigin(t *testing.T) {
	for _, test := range []struct {
		name      string
		origin    string
		fetchSite string
		want      bool
	}{
		{name: "matching origin", origin: "https://newpress.example", fetchSite: "same-origin", want: true},
		{name: "missing origin", fetchSite: "same-origin"},
		{name: "cross site fetch", origin: "https://newpress.example", fetchSite: "cross-site"},
		{name: "different scheme", origin: "http://newpress.example", fetchSite: "same-origin"},
		{name: "different host", origin: "https://other.example", fetchSite: "same-origin"},
		{name: "origin path", origin: "https://newpress.example/install", fetchSite: "same-origin"},
		{name: "origin credentials", origin: "https://user@newpress.example", fetchSite: "same-origin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _ := http.NewRequest(http.MethodPost, "https://newpress.example/install/", nil)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			if got := strictSetupOrigin(request, "https://newpress.example"); got != test.want {
				t.Fatalf("strictSetupOrigin() = %t, want %t", got, test.want)
			}
		})
	}

	request, _ := http.NewRequest(http.MethodPost, "https://newpress.example/install/", nil)
	request.Header.Add("Origin", "https://newpress.example")
	request.Header.Add("Origin", "https://newpress.example")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if strictSetupOrigin(request, "https://newpress.example") {
		t.Fatal("duplicate Origin headers were accepted")
	}
}

func TestValidSetupFormRejectsUnknownAndRepeatedFields(t *testing.T) {
	valid := url.Values{
		"setup_token":      {strings.Repeat("s", 40)},
		"username":         {"admin"},
		"email":            {"admin@example.test"},
		"display_name":     {"Admin"},
		"password":         {strings.Repeat("p", 16)},
		"confirm_password": {strings.Repeat("p", 16)},
	}
	if !validSetupForm(valid) {
		t.Fatal("complete single-value setup form was rejected")
	}

	unknown := make(url.Values, len(valid)+1)
	for key, values := range valid {
		unknown[key] = append([]string(nil), values...)
	}
	unknown.Set("unexpected", "value")
	if validSetupForm(unknown) {
		t.Fatal("unknown setup form field was accepted")
	}

	repeated := make(url.Values, len(valid))
	for key, values := range valid {
		repeated[key] = append([]string(nil), values...)
	}
	repeated.Add("setup_token", strings.Repeat("s", 40))
	if validSetupForm(repeated) {
		t.Fatal("repeated setup form field was accepted")
	}
}

func TestSetupTokenMatchesWithoutPrefixLeakage(t *testing.T) {
	if !setupTokenMatches(strings.Repeat("a", 40), strings.Repeat("a", 40)) {
		t.Fatal("equal tokens did not match")
	}
	for _, provided := range []string{"", strings.Repeat("a", 39), strings.Repeat("a", 40) + "b", strings.Repeat("a", 39) + "%61"} {
		if setupTokenMatches(strings.Repeat("a", 40), provided) {
			t.Fatalf("unexpected token match for %q", provided)
		}
	}
}

func TestSetupRateLimiterIsBoundedAndExpires(t *testing.T) {
	now := time.Date(2026, time.November, 19, 8, 0, 0, 0, time.UTC)
	limiter := setupRateLimiter{now: func() time.Time { return now }}
	for attempt := 0; attempt < setupRateMaximum; attempt++ {
		if !limiter.allow("192.0.2.8") {
			t.Fatalf("attempt %d unexpectedly rejected", attempt+1)
		}
	}
	if limiter.allow("192.0.2.8") {
		t.Fatal("request beyond configured limit was accepted")
	}
	if !limiter.allow("192.0.2.9") {
		t.Fatal("independent client address was rejected")
	}
	now = now.Add(setupRateWindow)
	if !limiter.allow("192.0.2.8") {
		t.Fatal("rate limit did not expire")
	}
}

func TestSetupHTTPRejectsCrossSiteAndOversizedRequestsWithPrivateHeaders(t *testing.T) {
	server := &Server{
		config: config.Config{
			DatabaseURL: "postgres://example.test/newpress",
			SetupToken:  strings.Repeat("s", 40),
			PublicURL:   "https://newpress.example",
		},
		clientIPs: newClientIPResolver(nil),
	}
	for _, test := range []struct {
		name             string
		query            string
		origin           string
		extraOrigin      string
		extraContentType string
		body             []byte
		contentType      string
		wantStatus       int
	}{
		{name: "cross site", origin: "https://attacker.example", body: []byte("setup_token=x"), contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusForbidden},
		{name: "missing origin", body: []byte("setup_token=x"), contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusForbidden},
		{name: "oversized body", origin: "https://newpress.example", body: []byte(strings.Repeat("x", setupRequestLimit+1)), contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusRequestEntityTooLarge},
		{name: "token in query", query: "?setup_token=never-in-url", wantStatus: http.StatusNotFound},
		{name: "duplicate origin", origin: "https://newpress.example", extraOrigin: "https://newpress.example", body: []byte(validSetupFormBody()), contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusForbidden},
		{name: "duplicate content type", origin: "https://newpress.example", contentType: "application/x-www-form-urlencoded", extraContentType: "application/x-www-form-urlencoded", body: []byte(validSetupFormBody()), wantStatus: http.StatusBadRequest},
		{name: "unknown form field", origin: "https://newpress.example", body: []byte(validSetupFormBody() + "&unexpected=value"), contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusBadRequest},
		{name: "repeated form field", origin: "https://newpress.example", body: []byte(validSetupFormBody() + "&setup_token=" + strings.Repeat("s", 40)), contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/install/"+test.query, bytes.NewReader(test.body))
			request.RemoteAddr = "192.0.2.15:54321"
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.extraOrigin != "" {
				request.Header.Add("Origin", test.extraOrigin)
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.extraContentType != "" {
				request.Header.Add("Content-Type", test.extraContentType)
			}
			response := httptest.NewRecorder()
			server.adminSetupSubmit(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			for header, want := range map[string]string{
				"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff",
			} {
				if got := response.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			if strings.Contains(response.Body.String(), server.config.SetupToken) || strings.Contains(response.Header().Get("Location"), server.config.SetupToken) {
				t.Fatal(fmt.Sprintf("setup token appeared in response"))
			}
		})
	}
}

// The HTTP tests use the normal auth manager and user store. Only the durable
// installation gate is substituted; real transaction behavior is covered by
// the PostgreSQL integration tests.
type setupHTTPUserStore struct {
	*auth.FileUserStore
	closed bool
}

func (store *setupHTTPUserStore) FirstAdminSetupAvailable(context.Context) (bool, error) {
	return !store.closed, nil
}

func (store *setupHTTPUserStore) ClaimFirstAdministrator(ctx context.Context, mutation auth.UserMutation, input auth.UserInput, passwordHash string) (auth.User, error) {
	if store.closed {
		return auth.User{}, auth.ErrFirstAdminSetupClosed
	}
	user, err := store.CreateUser(ctx, mutation, input, passwordHash)
	if err == nil {
		store.closed = true
	}
	return user, err
}

func newSetupHTTPServer(t *testing.T) (*Server, *setupHTTPUserStore) {
	t.Helper()
	users, err := auth.NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	store := &setupHTTPUserStore{FileUserStore: users}
	manager, err := auth.NewWithContext(t.Context(), auth.Config{Users: store})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		config: config.Config{
			DatabaseURL: "postgres://example.test/newpress",
			SetupToken:  strings.Repeat("s", 40),
			PublicURL:   "https://newpress.example",
		},
		auth: manager, clientIPs: newClientIPResolver(nil),
	}, store
}

func submitSetupHTTPForm(server *Server, form url.Values) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/install/", strings.NewReader(form.Encode()))
	request.RemoteAddr = "192.0.2.20:54321"
	request.Header.Set("Origin", server.config.PublicURL)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.adminSetupSubmit(response, request)
	return response
}

func TestSetupHTTPClaimCreatesAdministratorAndClosesInstaller(t *testing.T) {
	server, store := newSetupHTTPServer(t)
	form, err := url.ParseQuery(validSetupFormBody())
	if err != nil {
		t.Fatal(err)
	}
	response := submitSetupHTTPForm(server, form)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/login/" {
		t.Fatalf("successful setup status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), server.config.SetupToken) || strings.Contains(response.Body.String(), form.Get("password")) {
		t.Fatal("successful setup response exposed credentials or allowed caching")
	}
	if !server.auth.Configured() || !store.closed {
		t.Fatal("successful claim did not configure authentication and close setup")
	}
	if _, principal, err := server.auth.AuthenticateContext(t.Context(), "setup-owner", form.Get("username"), form.Get("password")); err != nil || !principal.HasRole("administrator") {
		t.Fatalf("claimed account could not log in as administrator: err=%v", err)
	}
	if repeated := submitSetupHTTPForm(server, form); repeated.Code != http.StatusNotFound {
		t.Fatalf("closed setup accepted another submission: status=%d", repeated.Code)
	}
	page := httptest.NewRecorder()
	server.adminSetupPage(page, httptest.NewRequest(http.MethodGet, "/install/", nil))
	if page.Code != http.StatusNotFound {
		t.Fatalf("closed setup page status=%d", page.Code)
	}
	if count, err := store.CountManagedUsers(t.Context()); err != nil || count != 1 {
		t.Fatalf("setup created an unexpected number of accounts: count=%d err=%v", count, err)
	}
}

func TestSetupHTTPInvalidCredentialsLeaveInstallerAvailable(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields map[string]string
		status int
	}{
		{name: "wrong setup token", fields: map[string]string{"setup_token": strings.Repeat("x", 40)}, status: http.StatusForbidden},
		{name: "weak password", fields: map[string]string{"password": "short", "confirm_password": "short"}, status: http.StatusBadRequest},
		{name: "password mismatch", fields: map[string]string{"confirm_password": "does not match"}, status: http.StatusBadRequest},
		{name: "invalid username", fields: map[string]string{"username": "contains spaces"}, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, store := newSetupHTTPServer(t)
			form, err := url.ParseQuery(validSetupFormBody())
			if err != nil {
				t.Fatal(err)
			}
			for field, value := range test.fields {
				form.Set(field, value)
			}
			response := submitSetupHTTPForm(server, form)
			if response.Code != test.status {
				t.Fatalf("invalid setup status=%d want=%d", response.Code, test.status)
			}
			if server.auth.Configured() || store.closed {
				t.Fatal("invalid submission closed the installer")
			}
			if count, err := store.CountManagedUsers(t.Context()); err != nil || count != 0 {
				t.Fatalf("invalid submission created an account: count=%d err=%v", count, err)
			}
		})
	}
}

func validSetupFormBody() string {
	return url.Values{
		"setup_token":      {strings.Repeat("s", 40)},
		"username":         {"admin"},
		"email":            {"admin@example.test"},
		"display_name":     {"Admin"},
		"password":         {strings.Repeat("p", 16)},
		"confirm_password": {strings.Repeat("p", 16)},
	}.Encode()
}
