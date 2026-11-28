package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

type featureAuthorityConfigStore struct {
	wechat.Store
	after func()
}

func (s *featureAuthorityConfigStore) ViewConfig(ctx context.Context) (wechat.Config, error) {
	v, err := wechat.ReadConfig(ctx, s.Store)
	runAutosaveBoundary(&s.after)
	return v, err
}

func featureAuthorityRequest(cookie *http.Cookie, ctx context.Context, query string) *http.Request {
	r := httptest.NewRequest("GET", "https://example.test/api/v1/admin/feature-status?feature="+query, nil).WithContext(ctx)
	r.AddCookie(cookie)
	return r
}

func TestFeatureStatusAuthorityConfigRead(t *testing.T) {
	for _, change := range []string{"logout", "fallback", "permission", "outage", "cancel", "module-restarted", "allowed"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySettingsManage})
			fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
			sessions.SessionStore = fault
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			changed := false
			s.wechat.Store = &featureAuthorityConfigStore{Store: s.wechat.Store, after: func() {
				changed = true
				aiUsageChange(t, s, manager, users, fault, cookie, change, "wechat", cancel)
			}}
			r := featureAuthorityRequest(cookie, ctx, "wechat")
			if change == "fallback" {
				token, _, err := manager.AuthenticateContext(t.Context(), "feature-fallback", actor.Username, "Account password for "+actor.Username+" 2026")
				if err != nil {
					t.Fatal(err)
				}
				r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
			}
			w, aborted := commentAuthorityServe(s.routes(), r)
			if !changed {
				t.Fatal("configuration read boundary not reached")
			}
			if change == "cancel" {
				if !aborted || w.Body.Len() != 0 {
					t.Fatal("cancelled feature request returned configuration", aborted, w.Code)
				}
				return
			}
			want := map[string]int{"logout": 401, "fallback": 401, "permission": 403, "outage": 503, "module-restarted": 200, "allowed": 200}[change]
			if aborted || w.Code != want || strings.Contains(w.Body.String(), `"wechat-sync"`) != (want == 200) || strings.Contains(w.Body.String(), "PRIVATE-SESSION-DETAIL") {
				t.Fatal("feature diagnostics escaped original authority", aborted, w.Code, want)
			}
		})
	}
}

func TestFeatureStatusAuthorityFinalOutput(t *testing.T) {
	for _, feature := range []string{"comments", "wechat", "community", "article-comments", "ai-related", "ai-usage", "currency"} {
		t.Run(feature, func(t *testing.T) {
			t.Parallel()
			s, _, manager, repository, cookie, actor := newPersistentAdminTestServer(t)
			query := feature
			if feature == "article-comments" || feature == "ai-related" {
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "final-feature", "Final feature")
				query += fmt.Sprintf("&object=%d", post.ID)
			}
			recorder := httptest.NewRecorder()
			writer := &aiEditorAuthorityHeaderWriter{ResponseWriter: recorder, after: func() {
				if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
					t.Error(err)
				}
			}}
			aborted := serveAIGenerationBoundary(s.routes(), writer, featureAuthorityRequest(cookie, t.Context(), query))
			if !aborted || recorder.Body.Len() != 0 {
				t.Fatal("feature status body sent after final revocation", aborted, recorder.Body.Len())
			}
		})
	}
}

type featureAuthorityRepository struct {
	content.Repository
	reads, states atomic.Int32
	once          sync.Once
	after         func()
	fail          atomic.Bool
	author        atomic.Int64
	missing       atomic.Bool
	afterState    func(int32)
}

func (r *featureAuthorityRepository) GetByID(ctx context.Context, id int64) (content.Post, error) {
	r.reads.Add(1)
	if r.missing.Load() {
		return content.Post{}, content.ErrNotFound
	}
	if r.fail.Load() {
		return content.Post{}, errors.New("PRIVATE-FEATURE-STORAGE-DETAIL")
	}
	p, err := r.Repository.GetByID(ctx, id)
	if author := r.author.Load(); author > 0 {
		p.AuthorID = author
	}
	if r.after != nil {
		r.once.Do(r.after)
	}
	return p, err
}

func (r *featureAuthorityRepository) GetPostState(ctx context.Context, id int64) (content.PostState, error) {
	n := r.states.Add(1)
	if r.missing.Load() {
		return content.PostState{}, content.ErrNotFound
	}
	if r.fail.Load() {
		return content.PostState{}, errors.New("PRIVATE-FEATURE-STORAGE-DETAIL")
	}
	p, err := content.ReadPostState(ctx, r.Repository, id)
	if author := r.author.Load(); author > 0 {
		p.AuthorID = author
	}
	if r.after != nil {
		r.once.Do(r.after)
	}
	if r.afterState != nil {
		r.afterState(n)
	}
	return p, err
}

func TestFeatureStatusAuthorityObjectRead(t *testing.T) {
	for _, feature := range []string{"article-comments", "ai-related"} {
		for _, change := range []string{"owner", "deleted", "permission", "logout", "outage", "storage", "cancel", "allowed"} {
			t.Run(feature+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, repository, _, owner := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "feature-object", "Private feature object")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				wrapped := &featureAuthorityRepository{Repository: repository}
				changed := false
				wrapped.after = func() {
					changed = true
					if change == "owner" {
						// Ordinary editor updates preserve AuthorID; model an external
						// repository ownership change in subsequent reads instead.
						wrapped.author.Store(owner.UserID)
					} else if change == "storage" {
						wrapped.fail.Store(true)
					} else if change == "deleted" {
						wrapped.missing.Store(true)
					} else {
						aiUsageChange(t, s, manager, users, fault, cookie, change, "", cancel)
					}
				}
				s.repository = wrapped
				w, aborted := commentAuthorityServe(s.routes(), featureAuthorityRequest(cookie, ctx, fmt.Sprintf("%s&object=%d", feature, post.ID)))
				if !changed {
					t.Fatal("object read boundary not reached")
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled object request disclosed status", aborted, w.Code)
					}
					return
				}
				want := map[string]int{"owner": 404, "deleted": 404, "permission": 403, "logout": 401, "outage": 503, "storage": 503, "allowed": 200}[change]
				if aborted || w.Code != want || strings.Contains(w.Body.String(), `"checked_at"`) != (want == 200) || strings.Contains(w.Body.String(), "PRIVATE-") {
					t.Fatal("object status escaped authority", aborted, w.Code, want)
				}
			})
		}
	}
}

func TestFeatureStatusObjectBodyReadBudget(t *testing.T) {
	for _, feature := range []string{"article-comments", "ai-related"} {
		t.Run(feature, func(t *testing.T) {
			s, _, _, repository, cookie, actor := newPersistentAdminTestServer(t)
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "feature-budget", "Feature budget")
			wrapped := &featureAuthorityRepository{Repository: repository}
			s.repository = wrapped
			w, aborted := commentAuthorityServe(s.routes(), featureAuthorityRequest(cookie, t.Context(), fmt.Sprintf("%s&object=%d", feature, post.ID)))
			want := int32(0)
			if feature == "article-comments" {
				want = 1
			}
			if aborted || w.Code != 200 || wrapped.reads.Load() != want {
				t.Fatal("unnecessary body copies for feature status", aborted, w.Code, wrapped.reads.Load(), want)
			}
		})
	}
}

func TestFeatureStatusAuthorityNarrowedSnapshot(t *testing.T) {
	for _, feature := range []string{"article-comments", "ai-related"} {
		t.Run(feature, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, _, owner := newPersistentAdminTestServer(t)
			_, users, _, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityContentWrite})
			post := createOwnedTestPost(t, repository, owner.UserID, owner.Label(), "feature-narrow", "Previously foreign snapshot")
			wrapped := &featureAuthorityRepository{Repository: repository}
			wrapped.after = func() {
				// The current post becomes owned, but the prepared snapshot was not.
				wrapped.author.Store(actor.UserID)
				if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Narrowed role", Capabilities: []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn}}}); err != nil {
					t.Fatal(err)
				}
			}
			s.repository = wrapped
			w, aborted := commentAuthorityServe(s.routes(), featureAuthorityRequest(cookie, t.Context(), fmt.Sprintf("%s&object=%d", feature, post.ID)))
			if aborted || w.Code != 404 || strings.Contains(w.Body.String(), `"checked_at"`) {
				t.Fatal("current ownership authorized a foreign snapshot", aborted, w.Code)
			}
		})
	}
}

func TestFeatureStatusAuthorityCapabilityAlternatives(t *testing.T) {
	for _, row := range []struct{ feature, capability string }{
		{"comments", auth.CapabilityCommentsModerate},
		{"community", auth.CapabilitySystemRead},
		{"community", auth.CapabilitySettingsManage},
		{"article-comments", auth.CapabilityContentRead},
		{"article-comments", auth.CapabilityContentWrite},
		{"article-comments", auth.CapabilityContentWriteOwn},
		{"ai-related", auth.CapabilityContentWrite},
		{"ai-related", auth.CapabilityContentWriteOwn},
		{"ai-queue", auth.CapabilityPluginsManage},
		{"ai-usage", auth.CapabilitySystemRead},
	} {
		t.Run(row.feature+"/"+row.capability, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
			_, _, _, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, row.capability})
			query := row.feature
			if row.feature == "article-comments" || row.feature == "ai-related" {
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "feature-capability", "Feature capability")
				query += fmt.Sprintf("&object=%d", post.ID)
			}
			w, aborted := commentAuthorityServe(s.routes(), featureAuthorityRequest(cookie, t.Context(), query))
			if aborted || w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("feature capability alternative rejected", aborted, w.Code)
			}
		})
	}
}

func TestFeatureStatusAuthorityPausedDiagnostics(t *testing.T) {
	for _, row := range []struct{ feature, module string }{
		{"article-comments", "comments"}, {"ai-related", "ai"}, {"ai-usage", "analytics"}, {"wechat", "wechat"}, {"comments", "comments"},
	} {
		t.Run(row.feature, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, cookie, actor := newPersistentAdminTestServer(t)
			accountAuthorityModule(t, s, row.module, false)
			query := row.feature
			if row.feature == "article-comments" || row.feature == "ai-related" {
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "feature-paused", "Feature paused")
				query += fmt.Sprintf("&object=%d", post.ID)
			}
			w, aborted := commentAuthorityServe(s.routes(), featureAuthorityRequest(cookie, t.Context(), query))
			if aborted || w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"paused"`) {
				t.Fatal("paused module prevented authorized diagnosis", aborted, w.Code)
			}
		})
	}
}

func TestFeatureStatusAuthorityLocalPreview(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	s.auth, _ = auth.New(auth.Config{})
	r := httptest.NewRequest("GET", "http://localhost/api/v1/admin/feature-status?feature=wechat", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w, aborted := commentAuthorityServe(s.routes(), r)
	if aborted || w.Code != 200 || !strings.Contains(w.Body.String(), `"read_only":true`) {
		t.Fatal("loopback diagnostics preview rejected", aborted, w.Code)
	}
	r = httptest.NewRequest("GET", "http://example.test/api/v1/admin/feature-status?feature=wechat", nil)
	r.RemoteAddr = "198.51.100.10:1234"
	w, aborted = commentAuthorityServe(s.routes(), r)
	if aborted || w.Code != 404 || strings.Contains(w.Body.String(), `"checked_at"`) {
		t.Fatal("remote preview exposed configuration", aborted, w.Code)
	}
}
