package httpserver

import (
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

func TestManagedRegistrationCodeOAuthNewAndExistingAccounts(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new-account"
		if existing {
			name = "existing-account"
		}
		t.Run(name, func(t *testing.T) {
			s, h, manager, _, _, _ := newPersistentAdminTestServer(t)

			p := site.DefaultRegistrationPolicy()
			p.Fields.Phone = "required"
			p.Fields.Email = "hidden"
			p.EmailVerification = false
			p.OAuthBindings = []string{"phone"}
			configureTestLoginProvider(t, s, "github", "fixture", "fixture-secret")
			p.Fields.RegistrationCode = "required"
			setEnhancedRegistration(t, s, "open", p)
			_, err := manager.MutateRegistrationCodes(t.Context(), auth.RegistrationCodeMutation{Action: "create", Codes: []string{"OAUTH-ONE"}, Rules: auth.RegistrationCodeRules{Enabled: true, MaxUses: 1}}, "admin")
			if err != nil {
				t.Fatal(err)
			}
			external := secretDigest("github:fixture:registration-code")
			var userID int64
			if existing {
				key, _ := auth.RandomCredential()
				u, err := manager.RegisterAccount(t.Context(), auth.UserInput{Username: "existing-oauth", DisplayName: "已有读者", OAuthProvider: "github", ExternalKey: external, RegistrationKey: secretDigest(key), RegistrationDigest: secretDigest(key), DisablePassword: true}, key)
				if err != nil {
					t.Fatal(err)
				}
				userID = u.ID
			}
			cookie := registrationBrowser(t, h)
			s.registration.mu.Lock()
			s.registration.prepare()
			s.registration.pending[secretDigest(cookie.Value)] = oauthPending{Provider: "github", ExternalKey: external, Name: "OAuth 读者", Fingerprint: secretDigest("fixture:fixture-secret"), BindingID: "code-binding", UserID: userID, Expires: time.Now().Add(time.Minute)}
			s.registration.mu.Unlock()
			requestID, _ := auth.RandomCredential()
			input := registrationInput{RequestID: requestID, BindingID: "code-binding", Phone: "+8613800138099"}
			post := func(want int) {
				t.Helper()
				r := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/access/bind", registrationBody(t, input), registrationHeaders)
				if r.Code != want {
					t.Fatalf("want %d, got %d: %s", want, r.Code, r.Body.String())
				}
			}
			if !existing {
				post(422)
				input.RegistrationCode = "UNKNOWN"
				post(422)
				input.RegistrationCode = "OAUTH-ONE"
			}
			post(200)
			post(200)
			u, err := manager.FindRegistration(t.Context(), "external", external)
			if err != nil || u.Phone != input.Phone || existing && u.ID != userID {
				t.Fatal("OAuth identity was not bound correctly", u.ID, err)
			}
			codes, err := manager.ListRegistrationCodes(t.Context())
			wantUses := int64(1)
			if existing {
				wantUses = 0
			}
			if err != nil || len(codes) != 1 || codes[0].Used != wantUses {
				t.Fatal("new registration must consume once; existing binding must not consume", codes, err)
			}
		})
	}
}
