package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
)

type auditReadyRepository struct {
	content.Repository
	calls int
}

func (r *auditReadyRepository) Ready(context.Context) error { r.calls++; return nil }

func TestReadinessRejectsRemoteBeforeStorage(t *testing.T) {
	s, h, _, _, _, _ := newPersistentAdminTestServer(t)
	repo := &auditReadyRepository{Repository: s.repository}
	s.repository = repo
	for _, host := range []string{"example.test", "localhost", "127.0.0.1"} {
		r := httptest.NewRequest("GET", "http://"+host+"/health/ready", nil)
		r.RemoteAddr = "198.51.100.2:4321"
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 || repo.calls != 0 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("remote probe reached storage: status=%d calls=%d", w.Code, repo.calls)
		}
	}
	if w := serveLocalReadiness(h); w.Code != 200 || repo.calls != 1 {
		t.Fatalf("local readiness failed: %d calls=%d", w.Code, repo.calls)
	}
}

func TestRateCapacityCannotEvictActiveBudgets(t *testing.T) {
	var limiter commentRateLimiter
	now := time.Now()
	for i := 0; i < commentRateKeyLimit; i++ {
		if ok, _ := limiter.allow(fmt.Sprint(i), 1, now); !ok {
			t.Fatal("unexpected initial rejection")
		}
	}
	if ok, _ := limiter.allow("new", 1, now); ok {
		t.Fatal("accepted new key at capacity")
	}
	if ok, _ := limiter.allow("0", 1, now); ok {
		t.Fatal("reset existing budget")
	}
	if ok, _ := limiter.allow("new", 1, now.Add(commentRateWindow)); !ok {
		t.Fatal("expired keys did not release capacity")
	}
}

func TestGuestCookieRotationCannotUnlockQuotaResource(t *testing.T) {
	s, h, _, repo, _, owner := newPersistentAdminTestServer(t)
	cfg := resourceFixtureConfig()
	cfg.Items[0].Variants[0].Groups = []resources.Group{{Conditions: []resources.Condition{{ID: "all", Kind: "public"}}}}
	p := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, Title: "Quota", Slug: "quota-security", Status: content.StatusPublish, Resources: &cfg})
	item := resources.FromMeta(p.Meta).Items[0]
	endpoint := fmt.Sprintf("https://example.test/api/v1/article-resources/%d/bundle", p.ID)
	for i := 0; i < 2; i++ {
		value := fmt.Sprintf("%040x", i+1)
		cookie := &http.Cookie{Name: componentCookie, Value: value}
		stateResponse := serveContentAPI(h, cookie, "GET", endpoint, "", nil)
		if stateResponse.Code != 200 {
			t.Fatalf("guest state projection failed: %d %s", stateResponse.Code, stateResponse.Body.String())
		}
		body := fmt.Sprintf(`{"action":"download","variant":"complete","link":"baidu","request":"test-request","revision":%q}`, resources.Hash(item))
		w := serveContentAPI(h, cookie, "POST", endpoint, body, map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": value})
		if w.Code != 401 {
			t.Fatalf("rotated visitor bypassed account quota: %d %s", w.Code, w.Body.String())
		}
	}
	state, err := s.resources.Backend.Read(t.Context())
	if err != nil || len(state.Deliveries) != 0 {
		t.Fatal("rejected guest created a delivery", err)
	}
}

func TestSessionCookieFailsSecureForPublicHTTP(t *testing.T) {
	for _, raw := range []string{"http://example.test", "https://example.test", ""} {
		s := &Server{}
		s.config.PublicURL = raw
		w := httptest.NewRecorder()
		s.setAdminCookie(w, "test-token")
		if !w.Result().Cookies()[0].Secure {
			t.Fatalf("insecure cookie for %q", raw)
		}
	}
}

func TestQuizRequiresAccountEvenWithoutDownloadQuota(t *testing.T) {
	s := &Server{}
	v := resources.Variant{Groups: []resources.Group{{Conditions: []resources.Condition{{Kind: "quiz"}}}}}
	r := resources.Resource{}
	allowed, _ := s.resourceAccess(t.Context(), content.Post{}, r, v, resourceFacts{})
	if allowed || !resourceRequiresAccount(r, v) {
		t.Fatal("anonymous quiz could use a replaceable visitor identity")
	}
	public := resources.Variant{Groups: []resources.Group{{Conditions: []resources.Condition{{Kind: "public"}}}}}
	allowed, _ = s.resourceAccess(t.Context(), content.Post{}, r, public, resourceFacts{})
	if !allowed {
		t.Fatal("unlimited public resources should remain available")
	}
}

func TestResourceSourceBudgetSurvivesCookieRotation(t *testing.T) {
	s, h, _, _, _, _ := newPersistentAdminTestServer(t)
	r := httptest.NewRequest("POST", "https://example.test/api/v1/article-resources/1/bundle", nil)
	key := "resource-source:" + s.commentGuard.sourceDigest(s.clientIPs.string(r))
	for i := 0; i < 90; i++ {
		s.accessRate.allow(key, 90, time.Now())
	}
	for i := 0; i < 2; i++ {
		cookie := &http.Cookie{Name: componentCookie, Value: fmt.Sprintf("%040x", i+1)}
		w := serveContentAPI(h, cookie, "POST", r.URL.String(), "", nil)
		if w.Code != 429 {
			t.Fatalf("cookie rotation bypassed source budget: %d", w.Code)
		}
	}
}

func TestGuestEmptyResourceSubmissionDoesNotWrite(t *testing.T) {
	s, h, _, repo, _, owner := newPersistentAdminTestServer(t)
	cfg := resourceFixtureConfig()
	cfg.Items[0].DailyLimit = 0
	p := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, Title: "Empty submit", Slug: "empty-submit-security", Status: content.StatusPublish, Resources: &cfg})
	item := resources.FromMeta(p.Meta).Items[0]
	endpoint := fmt.Sprintf("https://example.test/api/v1/article-resources/%d/bundle", p.ID)
	value := fmt.Sprintf("%040x", 1)
	body := fmt.Sprintf(`{"action":"submit","variant":"complete","revision":%q}`, resources.Hash(item))
	w := serveContentAPI(h, &http.Cookie{Name: componentCookie, Value: value}, "POST", endpoint, body, map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": value})
	if w.Code != 422 {
		t.Fatalf("empty submit: %d %s", w.Code, w.Body.String())
	}
	state, err := s.resources.Backend.Read(t.Context())
	if err != nil || len(state.Submissions) != 0 {
		t.Fatal("empty guest submission persisted", err)
	}
}
