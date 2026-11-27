package httpserver

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

func TestRegistrationCodeWorkbenchAuthorizationAndBatch(t *testing.T) {
	s, h, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	_, author, authorPrincipal := createAndLoginTestUser(t, manager, owner, "code-author", "ca@example.test", "作者", "author")
	body := `{"action":"generate","generator":{"count":3,"prefix":"TEST-","length":12,"charset":"friendly","group":4},"rules":{"enabled":true,"max_uses":2,"note":"测试批次"}}`
	endpoint := "https://example.test/api/v1/admin/registration-codes"
	for _, tc := range []struct {
		cookie       *http.Cookie
		csrf, origin string
		status       int
	}{{nil, "", "https://example.test", 401}, {author, authorPrincipal.CSRFToken, "https://example.test", 403}, {cookie, "", "https://example.test", 403}, {cookie, owner.CSRFToken, "https://other.test", 403}} {
		headers := workbenchHeaders(tc.csrf)
		headers["Origin"] = tc.origin
		r := serveContentAPI(h, tc.cookie, "POST", endpoint, body, headers)
		if r.Code != tc.status {
			t.Fatal(r.Code, tc.status, r.Body.String())
		}
	}
	for _, tc := range []struct {
		cookie *http.Cookie
		status int
	}{{nil, 401}, {author, 403}, {cookie, 200}} {
		r := serveContentAPI(h, tc.cookie, "GET", endpoint, "", nil)
		if r.Code != tc.status {
			t.Fatal("inventory authorization", r.Code)
		}
	}
	r := serveContentAPI(h, cookie, "POST", endpoint, body, workbenchHeaders(owner.CSRFToken))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var result struct{ Items []auth.RegistrationCode }
	decodeAPIResponse(t, r, &result)
	if len(result.Items) != 3 {
		t.Fatal("generation count")
	}
	patch := auth.RegistrationCodeMutation{Action: "update", Targets: []auth.RegistrationCodeTarget{{ID: result.Items[0].ID, Version: 1}, {ID: result.Items[1].ID, Version: 1}}}
	note := "新备注"
	patch.Patch.Note = &note
	encoded, _ := json.Marshal(patch)
	r = serveContentAPI(h, cookie, "POST", endpoint, string(encoded), workbenchHeaders(owner.CSRFToken))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	r = serveContentAPI(h, cookie, "POST", endpoint, string(encoded), workbenchHeaders(owner.CSRFToken))
	if r.Code != 409 {
		t.Fatal("stale batch accepted", r.Code)
	}
	page := serveCookieRequest(h, "GET", "https://example.test/admin/registration-codes/", cookie, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "chunnel-registration-codes.js") {
		t.Fatal("page unavailable", page.Code, page.Body.String())
	}
	for _, code := range result.Items {
		if strings.Contains(page.Body.String(), code.Code) {
			t.Fatal("code embedded in cacheable markup")
		}
	}
	r = serveContentAPI(h, cookie, "GET", endpoint+"?unknown=1", "", nil)
	if r.Code != 400 {
		t.Fatal("unknown query accepted")
	}
	r = serveContentAPI(h, cookie, "POST", endpoint, `{"action":"generate","unexpected":true}`, workbenchHeaders(owner.CSRFToken))
	if r.Code != 400 {
		t.Fatal("unknown field accepted", r.Code)
	}
	for _, oldField := range []string{`"managed":false`, `"legacy_available":true`, `"registration_hash":"fixed"`} {
		r = serveContentAPI(h, cookie, "POST", endpoint+"/policy", "{"+oldField+"}", workbenchHeaders(owner.CSRFToken))
		if r.Code != 400 {
			t.Fatalf("retired policy accepted: %s (%d)", oldField, r.Code)
		}
	}
	snapshot := s.siteSettings.Snapshot()
	policyBody := `{"revision":"` + snapshot.Revision + `","mode":"required","code_url":"/get-code/"}`
	r = serveContentAPI(h, cookie, "POST", endpoint+"/policy", policyBody, workbenchHeaders(owner.CSRFToken))
	if r.Code != 200 {
		t.Fatal("managed policy without old fixed code", r.Code, r.Body.String())
	}
	if s.siteSettings.Snapshot().Settings.RegistrationPolicy().Fields.RegistrationCode != "required" {
		t.Fatal("managed policy not saved")
	}
	r = serveContentAPI(h, cookie, "POST", endpoint+"/policy", policyBody, workbenchHeaders(owner.CSRFToken))
	if r.Code != 409 {
		t.Fatal("stale policy accepted")
	}
	r = serveContentAPI(h, nil, "GET", "https://example.test/api/v1/social/access", "", nil)
	for _, c := range result.Items {
		if strings.Contains(r.Body.String(), c.Code) || strings.Contains(r.Body.String(), c.ID) {
			t.Fatal("private inventory exposed")
		}
	}
}

func TestManagedRegistrationCodeRequiredReplayAndMail(t *testing.T) {
	s, h, manager, _, _, _ := newPersistentAdminTestServer(t)
	p := site.DefaultRegistrationPolicy()
	p.EmailVerification = false
	p.Fields.RegistrationCode = "required"
	setEnhancedRegistration(t, s, "open", p)
	_, err := manager.MutateRegistrationCodes(t.Context(), auth.RegistrationCodeMutation{Action: "create", Codes: []string{"HTTP-ONE", "HTTP-MAIL"}, Rules: auth.RegistrationCodeRules{Enabled: true, MaxUses: 1}}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	browser := registrationBrowser(t, h)
	key, _ := auth.RandomCredential()
	input := registrationInput{RequestID: key, Username: "code-reader", DisplayName: "读者", Email: "reader@example.test", Password: "Registration password 2026", ConfirmPassword: "Registration password 2026"}
	post := func() int {
		t.Helper()
		r := serveContentAPI(h, browser, "POST", "https://example.test/api/v1/social/access/register", registrationBody(t, input), registrationHeaders)
		return r.Code
	}
	if post() != 422 {
		t.Fatal("missing code accepted")
	}
	input.RegistrationCode = "unknown"
	if post() != 422 {
		t.Fatal("unknown code accepted")
	}
	input.RegistrationCode = "HTTP-ONE"
	input.Username = "owner"
	if post() != 409 {
		t.Fatal("username conflict not detected")
	}
	items, _ := manager.ListRegistrationCodes(t.Context())
	for _, c := range items {
		if c.Used != 0 {
			t.Fatal("failed input consumed code")
		}
	}
	input.Username = "code-reader"
	if post() != 200 {
		t.Fatal("valid code rejected")
	}
	if post() != 200 {
		t.Fatal("same request not reconciled")
	}
	items, _ = manager.ListRegistrationCodes(t.Context())
	for _, c := range items {
		if c.Code == "HTTP-ONE" && c.Used != 1 {
			t.Fatal("replay consumed twice")
		}
	}
	input.RequestID, _ = auth.RandomCredential()
	input.Username = "code-reader-two"
	input.Email = "other@example.test"
	if post() != 422 {
		t.Fatal("exhausted code reused")
	}
	// A delivered email invitation and a code are committed together.
	raw, _, err := manager.IssueAccess(t.Context(), auth.AccessInvite, "mail-reader@example.test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	input.Token = raw
	input.Kind = auth.AccessInvite
	input.RegistrationCode = "HTTP-MAIL"
	input.Username = "mail-reader"
	input.RequestID, _ = auth.RandomCredential()
	if post() != 200 || post() != 200 {
		t.Fatal("mail registration/replay failed")
	}
	items, _ = manager.ListRegistrationCodes(t.Context())
	for _, c := range items {
		if c.Used != 1 {
			t.Fatal("mail and code not committed together", c.Code, c.Used)
		}
	}
	// Old completion endpoint cannot bypass the managed policy.
	r := serveContentAPI(h, browser, "POST", "https://example.test/api/v1/social/access/complete", `{"kind":"invite","token":"`+raw+`","username":"bypass","password":"Registration password 2026"}`, registrationHeaders)
	if r.Code != 400 {
		t.Fatal("legacy bypass", r.Code)
	}
	// Optional empty values do not consume; hidden input is ignored.
	p.Fields.RegistrationCode = "optional"
	setEnhancedRegistration(t, s, "open", p)
	input.Token = ""
	input.Kind = ""
	input.RegistrationCode = ""
	input.Username = "optional-reader"
	input.Email = "optional@example.test"
	input.RequestID, _ = auth.RandomCredential()
	if post() != 200 {
		t.Fatal("optional empty rejected")
	}
	p.Fields.RegistrationCode = "hidden"
	setEnhancedRegistration(t, s, "open", p)
	input.RegistrationCode = "HTTP-ONE"
	input.Username = "hidden-reader"
	input.Email = "hidden@example.test"
	input.RequestID, _ = auth.RandomCredential()
	if post() != 200 {
		t.Fatal("hidden code incorrectly validated")
	}
}

func TestRegistrationCodesBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_REGISTRATION_CODES_BROWSER") != "1" {
		t.Skip("opt-in synthetic browser fixture")
	}
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	s.config.PublicURL = "http://127.0.0.1:19429"
	p := site.DefaultRegistrationPolicy()
	p.EmailVerification = false
	setEnhancedRegistration(t, s, "open", p)
	listener, err := net.Listen("tcp", "127.0.0.1:19429")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go server.Serve(listener)
	t.Log("Registration codes fixture: http://127.0.0.1:19429; owner / Persistent owner password 2026")
	select {
	case <-t.Context().Done():
	case <-time.After(25 * time.Minute):
	}
}
