package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/pluginshared"
)

func sharedReviewRequest(h http.Handler, cookie *http.Cookie, path string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for _, value := range headers {
		r.Header.Add(sharedReviewUserHeader, value)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPluginSharedReviewPageAccessAndStorageModes(t *testing.T) {
	s, h, c, p := newReviewedTaskHTTPTestServer(t)
	store := newSharedMaintenanceHTTPStore(t)
	s.SetPluginTaskStore(store)
	path := "/admin/plugin-records/?plugin=reader&job=" + strings.Repeat("a", 64)
	w := sharedReviewRequest(h, c, path)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal(w.Code, w.Header())
	}
	for _, value := range []string{`data-shared-review`, `data-available="true"`, `data-user-id="` + strconv.FormatInt(p.UserID, 10) + `"`, `value="reader"`, `name="job"`, `newpress-plugin-records.css`, `newpress-plugin-records.js`, `隔离与修复审计`, `不提供配置、原始数据`} {
		if !strings.Contains(w.Body.String(), value) {
			t.Fatal("missing review page contract", value)
		}
	}
	if store.calls != 0 || strings.Contains(w.Body.String(), "private-slot") || strings.Contains(w.Body.String(), "offline-operator") {
		t.Fatal("page must not preload private results")
	}
	for _, path := range []string{"/admin/plugin-records/?plugin=../bad", "/admin/plugin-records/?job=bad", "/admin/plugin-records/?plugin=a&plugin=b", "/admin/plugin-records/?raw=1"} {
		if w := sharedReviewRequest(h, c, path); w.Code != 400 {
			t.Fatal("invalid page query", path, w.Code)
		}
	}
	if w := sharedReviewRequest(h, nil, "/admin/plugin-records/"); w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/admin/login/") {
		t.Fatal("anonymous review page", w.Code)
	}
	s.SetPluginTaskStore(store.Queue)
	w = sharedReviewRequest(h, c, "/admin/plugin-records/")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `data-available="false"`) || !strings.Contains(w.Body.String(), "当前存储不提供共享记录") || strings.Contains(w.Body.String(), "data-review-form") {
		t.Fatal("file-mode review", w.Code, w.Body.String())
	}
	legacy, lh, _, lc, _ := newCommentHTTPTestServer(t)
	legacy.SetPluginTaskStore(store)
	if w := sharedReviewRequest(lh, lc, "/admin/plugin-records/"); w.Code != 403 {
		t.Fatal("bootstrap account review", w.Code)
	}
}

func TestPluginSharedReviewIdentityAndSessionProjection(t *testing.T) {
	s, h, c, p := newReviewedTaskHTTPTestServer(t)
	store := newSharedMaintenanceHTTPStore(t)
	s.SetPluginTaskStore(store)
	owner := strconv.FormatInt(p.UserID, 10)
	for _, path := range []string{"/api/v1/admin/plugin-shared-records", "/api/v1/admin/plugin-shared-quarantines", "/api/v1/admin/plugin-shared-review-session"} {
		before := store.calls
		for _, headers := range [][]string{{"0"}, {"+" + owner}, {"0" + owner}, {owner, owner}, {""}, {owner + "," + owner}} {
			if w := sharedReviewRequest(h, c, path, headers...); w.Code != 409 || strings.Contains(w.Body.String(), "offline-operator") {
				t.Fatal("unbound read", path, headers, w.Code)
			}
		}
		if store.calls != before {
			t.Fatal("changed identity reached storage")
		}
		w := sharedReviewRequest(h, c, path, owner)
		if w.Code != 200 || w.Header().Get(sharedReviewUserHeader) != owner || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal(path, w.Code, w.Header(), w.Body.String())
		}
	}
	before := store.calls
	w := sharedReviewRequest(h, c, "/api/v1/admin/plugin-shared-review-session", owner)
	var data map[string]any
	if json.Unmarshal(w.Body.Bytes(), &data) != nil || len(data) != 2 || data["user_id"] != owner || data["available"] != true || store.calls != before {
		t.Fatal("session check read records or exposed extra state", w.Body.String())
	}
	if w := sharedReviewRequest(h, c, "/api/v1/admin/plugin-shared-review-session?plugin=reader", owner); w.Code != 400 {
		t.Fatal("unknown session query", w.Code)
	}
	s.SetPluginTaskStore(store.Queue)
	w = sharedReviewRequest(h, c, "/api/v1/admin/plugin-shared-review-session", owner)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatal("file mode session", w.Code, w.Body.String())
	}
}

type revokingSharedReviewStore struct {
	*sharedMaintenanceHTTPStore
	afterRead func()
}

func (s *revokingSharedReviewStore) InspectSharedRecords(ctx context.Context, q pluginshared.Query) (pluginshared.Page, error) {
	p, err := s.sharedMaintenanceHTTPStore.InspectSharedRecords(ctx, q)
	s.afterRead()
	return p, err
}
func (s *revokingSharedReviewStore) ListSharedQuarantines(ctx context.Context, q pluginshared.Query) (pluginshared.QuarantinePage, error) {
	p, err := s.sharedMaintenanceHTTPStore.ListSharedQuarantines(ctx, q)
	s.afterRead()
	return p, err
}

func TestPluginSharedReviewRechecksRevocationAfterStorageRead(t *testing.T) {
	for _, endpoint := range []string{"plugin-shared-records", "plugin-shared-quarantines"} {
		t.Run(endpoint, func(t *testing.T) {
			s, h, _, actor := newReviewedTaskHTTPTestServer(t)
			password := "Shared record reviewer password 2026"
			user, err := s.auth.CreateUser(t.Context(), actor, auth.UserInput{Username: "record-reviewer", Email: "records@example.test", Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"administrator"}}, password)
			if err != nil {
				t.Fatal(err)
			}
			token, reviewer, err := s.auth.AuthenticateContext(t.Context(), "reviewer", user.Username, password)
			if err != nil {
				t.Fatal(err)
			}
			store := &revokingSharedReviewStore{sharedMaintenanceHTTPStore: newSharedMaintenanceHTTPStore(t), afterRead: func() {
				_, err = s.auth.UpdateUser(t.Context(), actor, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, Locale: user.Locale, Status: user.Status, RoleSlugs: []string{"editor"}})
				if err != nil {
					t.Fatal(err)
				}
			}}
			s.SetPluginTaskStore(store)
			cookie := &http.Cookie{Name: auth.CookieName, Value: token}
			w := sharedReviewRequest(h, cookie, "/api/v1/admin/"+endpoint, strconv.FormatInt(reviewer.UserID, 10))
			if w.Code != 403 || strings.Contains(w.Body.String(), `"items"`) || store.calls != 1 {
				t.Fatal("revoked read leaked results", w.Code, w.Body.String(), store.calls)
			}
			if w := sharedReviewRequest(h, cookie, "/admin/plugin-records/"); w.Code != 403 {
				t.Fatal("revoked page", w.Code)
			}
			if w := sharedReviewRequest(h, cookie, "/api/v1/admin/plugin-shared-review-session"); w.Code != 403 {
				t.Fatal("revoked session check", w.Code)
			}
		})
	}
}

func TestPluginSharedReviewSessionRateIsIndependentOfRecordQueries(t *testing.T) {
	s, h, c, p := newReviewedTaskHTTPTestServer(t)
	store := newSharedMaintenanceHTTPStore(t)
	s.SetPluginTaskStore(store)
	owner := strconv.FormatInt(p.UserID, 10)
	for i := 0; i <= 120; i++ {
		want := 200
		if i == 120 {
			want = 429
		}
		if w := sharedReviewRequest(h, c, "/api/v1/admin/plugin-shared-review-session", owner); w.Code != want {
			t.Fatal(i, w.Code)
		}
	}
	if store.calls != 0 {
		t.Fatal("session checks scanned retained records")
	}
	if w := sharedReviewRequest(h, c, "/api/v1/admin/plugin-shared-records", owner); w.Code != 200 || store.calls != 1 {
		t.Fatal("session checks consumed record budget", w.Code, store.calls)
	}
	for _, path := range []string{"/admin/plugin-records/", "/api/v1/admin/plugin-shared-review-session"} {
		r := httptest.NewRequest(http.MethodPost, "https://example.test"+path, nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatal("write route exists", path, w.Code)
		}
	}
}
