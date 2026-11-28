package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/recognition"
)

// Opt-in real pages and reads, disposable accounts and stores. The browser
// intercepts every business write, except the read-only settings preview.
func TestAccountAdminBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_ACCOUNT_ADMIN_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated account administration browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, users, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "account-reader", "account-reader@example.test", "账户读者", "subscriber")
	paths := walletBrowserStores(t, s, owner.UserID)
	backend, err := recognition.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetRecognitionService(&recognition.Service{Backend: backend, Users: s.auth, Content: s.repository, Currencies: s.currencyCatalog})
	if _, err = users.MutateRegistrationCodes(t.Context(), auth.RegistrationCodeMutation{Action: "create", Codes: []string{"BROWSER-001", "BROWSER-002"}, Rules: auth.RegistrationCodeRules{Enabled: true, MaxUses: 5, Note: "隔离验收"}}, owner.Username); err != nil {
		t.Fatal(err)
	}

	readState := func() []byte {
		accounts, err := users.ListUsers(t.Context(), auth.UserQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		codes, err := users.ListRegistrationCodes(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		recognitionState, err := backend.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		state := map[string]any{"accounts": accounts, "site_settings": s.siteSettings.Snapshot(), "codes": codes, "access": s.accessConfig(), "verification": s.verification.Store.Snapshot(), "recognition": recognitionState}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			state[filepath.Base(path)] = string(data)
		}
		values := s.themes.SettingsSnapshot().Values
		delete(values, "visual_style")
		state["theme_settings"] = values
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := readState()
	var blocked atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login/{role}", func(w http.ResponseWriter, r *http.Request) {
		cookie := ownerCookie
		if r.PathValue("role") == "reader" {
			cookie = readerCookie
		} else if r.PathValue("role") == "guest" {
			expired := *ownerCookie
			expired.Value, expired.MaxAge = "", -1
			cookie = &expired
		}
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/users/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /fixture/native-users", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/fixture/users-result?"+r.URL.RawQuery, http.StatusSeeOther)
	})
	// Browser route interception applies only to the first request in a redirect
	// chain. Render a disposable native-form receipt here without updating users.
	mux.HandleFunc("GET /fixture/users-result", func(w http.ResponseWriter, r *http.Request) {
		request := r.Clone(r.Context())
		request.URL.Path = "/admin/users/"
		query := request.URL.Query()
		query.Del("fixture_version")
		query.Del("fixture_name")
		query.Del("fixture_rotate")
		request.URL.RawQuery = query.Encode()
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		body := result.Body.String()
		version, err := strconv.ParseUint(r.URL.Query().Get("fixture_version"), 10, 64)
		if err == nil && version > 0 {
			body = regexp.MustCompile(`name="version" value="\d+"`).ReplaceAllString(body, `name="version" value="`+strconv.FormatUint(version, 10)+`"`)
		}
		if r.URL.Query().Has("fixture_name") {
			body = regexp.MustCompile(`name="display_name" value="[^"]*"`).ReplaceAllStringFunc(body, func(string) string {
				return `name="display_name" value="` + html.EscapeString(r.URL.Query().Get("fixture_name")) + `"`
			})
		}
		if r.URL.Query().Get("fixture_rotate") == "true" {
			body = strings.ReplaceAll(body, owner.CSRFToken, "fixture-rotated-csrf")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(result.Code)
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("POST /fixture/style/{style}", func(w http.ResponseWriter, r *http.Request) {
		style := r.PathValue("style")
		if style != "classic" && style != "liquid-glass" {
			http.NotFound(w, r)
			return
		}
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) > 0 {
			http.Error(w, "cannot change fixture style", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" && !(r.Method == "POST" && r.URL.Path == "/api/v1/admin/users/preview") {
			blocked.Add(1)
			http.Error(w, "browser mutation was not intercepted", 405)
			return
		}
		handler.ServeHTTP(w, r)
	})
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	writeEvidence := func(name string, value any) {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeEvidence("fixture.json", map[string]any{"url": host.URL, "login": host.URL + "/fixture/login/owner", "owner_id": owner.UserID, "reader_id": reader.UserID, "username": owner.Username, "reader_username": reader.Username})
	t.Log("account admin browser fixture ready", host.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(55 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("account admin browser fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(directory, "stop")); err == nil {
				after := readState()
				if !reflect.DeepEqual(before, after) || blocked.Load() != 0 {
					t.Fatalf("fixture state changed or %d writes escaped browser interception", blocked.Load())
				}
				digest := sha256.Sum256(after)
				writeEvidence("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "currency_accounts_ledger_and_receipts_unchanged": true, "progression_accounts_entries_receipts_and_orders_unchanged": true, "settings_unchanged": true, "codes_access_verification_recognition_unchanged": true, "theme_settings_unchanged_except_fixture_style": true, "unintercepted_mutations": blocked.Load(), "state_sha256": hex.EncodeToString(digest[:])})
				return
			}
		}
	}
}
