package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
)

// Session validation succeeds; the subsequent subscription authorization lookup
// sees the injected state, modelling revocation or a storage failure mid-request.
type subscriptionLookupStore struct {
	auth.UserStore
	armed   bool
	calls   int
	failure error
	denied  bool
}

func (s *subscriptionLookupStore) GetUser(ctx context.Context, id int64) (auth.User, error) {
	u, err := s.UserStore.GetUser(ctx, id)
	if s.armed {
		s.calls++
		if s.calls > 1 {
			if s.failure != nil {
				return auth.User{}, s.failure
			}
			if s.denied {
				u.Status = "disabled"
			}
		}
	}
	return u, err
}

func TestPluginSubscriptionHTTPAuthorization(t *testing.T) {
	s, handler, _, _, _ := newCommentHTTPTestServer(t)
	installTaskFixture(t, s)
	users, err := auth.NewFileUserStore("")
	if err != nil {
		t.Fatal(err)
	}
	store := &subscriptionLookupStore{UserStore: users}
	hash, err := auth.HashPassword("subscription fixture password 2026")
	if err != nil {
		t.Fatal(err)
	}
	s.auth, err = auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: store, BootstrapEmail: "owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	token, p, err := s.auth.Authenticate("subscription-tests", "owner", "subscription fixture password 2026")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}
	_, cfg, _, err := s.plugins.Configuration("task-fixture")
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.plugins.CurrentTaskEpoch("task-fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		failure error
		denied  bool
		csrf    string
		status  int
		code    string
	}{
		{"csrf", nil, false, "", 403, "auth.csrf_validation_failed"},
		{"storage", errors.New("private database failure"), false, p.CSRFToken, 503, "plugins.subscription_unavailable"},
		{"missing", auth.ErrUserNotFound, false, p.CSRFToken, 403, "plugins.subscription_identity"},
		{"revoked", nil, true, p.CSRFToken, 403, "plugins.subscription_identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.armed = true
			store.calls = 0
			store.failure = tc.failure
			store.denied = tc.denied
			body, _ := json.Marshal(map[string]any{"configuration": cfg, "subscribe_events": true, "expected_user_id": p.UserID})
			response := serveAdminCommentAPI(handler, cookie, tc.csrf, http.MethodPost, "https://example.test/api/v1/admin/plugins/task-fixture/configuration", string(body), "", "https://example.test")
			store.armed = false
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.code) {
				t.Fatalf("response: %d %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private database failure") {
				t.Fatal("storage details leaked")
			}
			after, e := s.plugins.CurrentTaskEpoch("task-fixture")
			if e != nil || after != before {
				t.Fatal("failed request changed epoch", e)
			}
			_, current, _, e := s.plugins.Configuration("task-fixture")
			if e != nil || !reflect.DeepEqual(current, cfg) {
				t.Fatal("failed request changed configuration", e)
			}
		})
	}
}
