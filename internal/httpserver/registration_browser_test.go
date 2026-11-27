package httpserver

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

// Opt-in loopback fixture for manual browser QA. All identities and settings
// are test-owned and no credentials are sent to real OAuth or SMS services.
func TestRegistrationBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_REGISTRATION_BROWSER_FIXTURE") != "1" {
		t.Skip("manual browser fixture")
	}
	s, h, _, _, owner, _ := newPersistentAdminTestServer(t)
	s.config.PublicURL = "http://127.0.0.1:8097"
	p := site.DefaultRegistrationPolicy()
	p.EmailVerification = false
	p.Fields.Email = "optional"
	p.Fields.Phone = "optional"
	p.Fields.InvitationCode = "optional"
	p.Fields.RegistrationCode = "optional"
	rp := p.Referrals()
	rp.Enabled = true
	p.Referral = &rp
	if _, err := s.auth.MutateRegistrationCodes(t.Context(), auth.RegistrationCodeMutation{Action: "create", Codes: []string{"register-preview"}, Rules: auth.RegistrationCodeRules{Enabled: true}}, "admin"); err != nil {
		t.Fatal(err)
	}
	p.RegistrationCodeURL = "/explore/"
	for _, id := range site.LoginProviderOrder {

		configureTestLoginProvider(t, s, id, "fixture-only", "fixture-only")
	}
	setEnhancedRegistration(t, s, "open", p)
	server := &http.Server{Addr: "127.0.0.1:8097", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_fixture/admin" {
			http.SetCookie(w, owner)
			http.Redirect(w, r, "/admin/settings/", 303)
			return
		}
		h.ServeHTTP(w, r)
	})}
	go func() { <-time.After(20 * time.Minute); _ = server.Shutdown(context.Background()) }()
	t.Log("registration preview ready on loopback port 8097")
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
