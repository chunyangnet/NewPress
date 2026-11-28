package httpserver

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

func TestOAuthPendingRechecksAccountAccess(t *testing.T) {
	for _, change := range []string{"unchanged_binding", "unbound", "rebound", "suspended"} {
		t.Run(change, func(t *testing.T) {
			s, h, manager, _, _, owner := newPersistentAdminTestServer(t)
			policy := site.DefaultRegistrationPolicy()
			policy.Fields.Phone = "required"
			policy.Fields.Email = "hidden"
			policy.EmailVerification = false
			policy.OAuthBindings = []string{"phone"}
			configureTestLoginProvider(t, s, "github", "fixture", "fixture-secret")
			setEnhancedRegistration(t, s, "open", policy)
			user, _, _ := createAndLoginTestUser(t, manager, owner, "pending-reader", "pending-reader@example.test", "Pending reader", "subscriber")
			user, err := manager.UpdateOAuthBinding(t.Context(), user, "github", secretDigest("github:fixture:123"))
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			s.registration.client = &http.Client{Transport: oauthTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				switch r.URL.String() {
				case oauthEndpoints["github"].Token:
					return oauthTestResponse(`{"access_token":"access-fixture"}`), nil
				case oauthEndpoints["github"].User:
					return oauthTestResponse(`{"id":123,"name":"Pending reader"}`), nil
				default:
					t.Fatalf("unexpected OAuth request: %s", r.URL)
					return nil, nil
				}
			})}

			// Use an unauthenticated browser and the real start/callback handlers
			// so the pending identity is obtained through a valid OAuth exchange.
			cookie := registrationBrowser(t, h)
			start := serveContentAPI(h, cookie, http.MethodGet, "https://example.test/auth/github/start", "", nil)
			if start.Code != http.StatusFound {
				t.Fatalf("OAuth start: %d %s", start.Code, start.Body.String())
			}
			redirect, err := url.Parse(start.Header().Get("Location"))
			if err != nil || redirect.Query().Get("state") == "" {
				t.Fatalf("invalid OAuth redirect: %v", err)
			}
			callback := "https://example.test/auth/github/callback?code=authorization-code&state=" + url.QueryEscape(redirect.Query().Get("state"))
			response := serveContentAPI(h, cookie, http.MethodGet, callback, "", nil)
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/account/access/?flow=bind" || calls != 2 {
				t.Fatalf("OAuth callback: %d %s, calls=%d", response.Code, response.Header().Get("Location"), calls)
			}
			for _, c := range response.Result().Cookies() {
				if c.Name == auth.CookieName && c.MaxAge > 0 {
					t.Fatal("OAuth callback issued a session before binding")
				}
			}
			const pendingURL = "https://example.test/api/v1/social/access/pending"
			before := serveContentAPI(h, cookie, http.MethodGet, pendingURL, "", nil)
			var pending struct {
				BindingID string `json:"binding_id"`
				Email     string `json:"email"`
				Phone     string `json:"phone"`
				Username  string `json:"username"`
				Existing  bool   `json:"existing"`
			}
			if before.Code != http.StatusOK {
				t.Fatalf("initial pending identity: %d %s", before.Code, before.Body.String())
			}
			decodeAPIResponse(t, before, &pending)
			if !pending.Existing || pending.BindingID == "" || pending.Email != user.Email || pending.Username != user.Username {
				t.Fatalf("initial pending identity is incomplete: %+v", pending)
			}

			// Account changes after authorization must not make newly saved
			// private contact details available to a revoked OAuth identity.
			user, err = manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{
				Username: user.Username, Email: "updated-pending@example.test", DisplayName: user.DisplayName,
				Status: auth.UserStatusActive, Locale: user.Locale, RoleSlugs: []string{"subscriber"},
				Account: &auth.UserAccountInput{Phone: "+8613800138099"},
			})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "unbound", "rebound":
				user, err = manager.AdminRemoveOAuthBinding(t.Context(), owner, user, "github")
				if err == nil && change == "rebound" {
					user, err = manager.UpdateOAuthBinding(t.Context(), user, "github", secretDigest("github:fixture:456"))
				}
			case "suspended":
				user, err = manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{
					Username: user.Username, Email: user.Email, DisplayName: user.DisplayName,
					Status: auth.UserStatusSuspended, Locale: user.Locale, RoleSlugs: []string{"subscriber"},
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			after := serveContentAPI(h, cookie, http.MethodGet, pendingURL, "", nil)
			if change == "unchanged_binding" {
				if after.Code != http.StatusOK {
					t.Fatalf("valid pending identity rejected: %d %s", after.Code, after.Body.String())
				}
				bindingID := pending.BindingID
				decodeAPIResponse(t, after, &pending)
				if pending.BindingID != bindingID || pending.Email != user.Email || pending.Phone != user.Phone || pending.Username != user.Username {
					t.Fatalf("valid pending identity lost account fields: %+v", pending)
				}
				return
			}
			if after.Code != http.StatusUnauthorized || !strings.Contains(after.Body.String(), "auth.binding_expired") {
				t.Errorf("revoked pending identity accepted: %d %s", after.Code, after.Body.String())
			}
			for _, private := range []string{user.Email, user.Phone, user.Username, pending.BindingID} {
				if private != "" && strings.Contains(after.Body.String(), private) {
					t.Errorf("revoked pending identity disclosed account data: %q", private)
				}
			}
		})
	}
}
