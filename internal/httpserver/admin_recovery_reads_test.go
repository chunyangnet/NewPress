package httpserver

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
)

type recoveryReadRepository struct {
	content.Repository
	posts, terms, probes int
}

func (r *recoveryReadRepository) ListAdmin(context.Context, content.AdminQuery) (content.AdminList, error) {
	r.posts++
	return content.AdminList{}, errors.New("unused content counts unavailable")
}
func (r *recoveryReadRepository) ListTerms(context.Context, content.TermQuery) (content.TermList, error) {
	r.terms++
	return content.TermList{}, errors.New("unused taxonomy counts unavailable")
}
func (r *recoveryReadRepository) Ready(context.Context) error {
	r.probes++
	return errors.New("unused probe unavailable")
}

func TestAdminRecoverySkipsUnusedSummaryReads(t *testing.T) {
	for _, kind := range []string{"paused", "missing", "questions", "plugin", "progression"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, cookie, _ := newPersistentAdminTestServer(t)
			path, success := "/admin/progression/", 503
			if kind == "progression" {
				s.progression = nil
			} else {
				path, success, _ = diagnosticRecoveryFixture(t, s, kind)
			}
			wrapped := &recoveryReadRepository{Repository: repository}
			s.repository = wrapped
			w, aborted := commentAuthorityServe(s.routes(), diagnosticRequest(path, cookie, t.Context()))
			if aborted || w.Code != success || !strings.Contains(w.Body.String(), "data-feature-status") {
				t.Fatal("unused unavailable summary broke recovery", aborted, w.Code, success)
			}
			if wrapped.posts != 0 || wrapped.terms != 0 || wrapped.probes != 0 {
				t.Fatal("recovery read unrendered counts/probes", wrapped.posts, wrapped.terms, wrapped.probes)
			}
		})
	}
}

func TestAdminRecoveryUnavailablePluginManager(t *testing.T) {
	s, _, _, _, cookie, _ := newPersistentAdminTestServer(t)
	s.plugins = nil
	w, aborted := commentAuthorityServe(s.routes(), diagnosticRequest("/admin/plugins/ai-pro-max/", cookie, t.Context()))
	if aborted || w.Code != 503 || !strings.Contains(w.Body.String(), "data-feature-status") {
		t.Fatal("missing plugin manager crashed recovery", aborted, w.Code)
	}
}

type recoveryContextKey struct{}

type recoveryBadgeStore struct {
	comment.Store
	calls   int
	ctx     context.Context
	waiting chan struct{}
	late    bool
	budget  time.Duration
}

func (s *recoveryBadgeStore) Snapshot(ctx context.Context) (comment.Snapshot, error) {
	s.calls++
	s.ctx = ctx
	if deadline, ok := ctx.Deadline(); ok {
		s.budget = time.Until(deadline)
	}
	if s.waiting != nil {
		close(s.waiting)
		<-ctx.Done()
		return comment.Snapshot{}, ctx.Err()
	}
	if s.late && s.budget > 0 {
		<-ctx.Done()
	}
	return comment.Snapshot{Total: 7, StatusCounts: map[string]int{comment.StatusPending: 7}}, nil
}

func TestAdminRecoveryReadsOnlyVisibleBadge(t *testing.T) {
	for _, name := range []string{"hidden", "visible", "paused-comments", "current-paused-comments"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			caps := []string{auth.CapabilityAdminAccess, auth.CapabilitySettingsManage}
			if name != "hidden" {
				caps = append(caps, auth.CapabilityCommentsModerate)
			}
			_, _, _, cookie, _ := dashboardAuthorityIdentity(t, s, caps)
			accountAuthorityModule(t, s, "wechat", false)
			path := "/admin/wechat/"
			if name == "paused-comments" || name == "current-paused-comments" {
				accountAuthorityModule(t, s, "comments", false)
			}
			if name == "current-paused-comments" {
				path = "/admin/comments/"
			}
			wrapped := &recoveryBadgeStore{Store: s.comments}
			s.comments = wrapped
			w, aborted := commentAuthorityServe(s.routes(), diagnosticRequest(path, cookie, t.Context()))
			want := 0
			visible := name == "visible" || name == "current-paused-comments"
			if visible {
				want = 1
			}
			if aborted || w.Code != 200 || wrapped.calls != want || strings.Contains(w.Body.String(), `<b class="has-attention">7</b>`) != visible {
				t.Fatal("badge reads or rendering changed", aborted, w.Code, wrapped.calls, want)
			}
		})
	}
}

func TestAdminRecoveryDiscardsLateBadge(t *testing.T) {
	s, _, _, _, cookie, _ := newPersistentAdminTestServer(t)
	accountAuthorityModule(t, s, "wechat", false)
	wrapped := &recoveryBadgeStore{Store: s.comments, late: true}
	s.comments = wrapped
	w, aborted := commentAuthorityServe(s.routes(), diagnosticRequest("/admin/wechat/", cookie, t.Context()))
	if aborted || w.Code != 200 || wrapped.calls != 1 || wrapped.budget <= 0 || wrapped.budget > 500*time.Millisecond || strings.Contains(w.Body.String(), `<b class="has-attention">7</b>`) || !strings.Contains(w.Body.String(), "data-feature-status") {
		t.Fatal("expired optional badge broke recovery or reached output", aborted, w.Code, wrapped.calls, wrapped.budget)
	}
}

func TestAdminRecoveryUnavailableBadgeStore(t *testing.T) {
	s, _, _, _, cookie, _ := newPersistentAdminTestServer(t)
	accountAuthorityModule(t, s, "wechat", false)
	s.comments = nil
	w, aborted := commentAuthorityServe(s.routes(), diagnosticRequest("/admin/wechat/", cookie, t.Context()))
	if aborted || w.Code != 200 || !strings.Contains(w.Body.String(), "data-feature-status") {
		t.Fatal("unavailable optional badge store broke recovery", aborted, w.Code)
	}
}

func TestAdminRecoveryBadgeUsesRequestContext(t *testing.T) {
	s, _, _, _, cookie, _ := newPersistentAdminTestServer(t)
	accountAuthorityModule(t, s, "wechat", false)
	wrapped := &recoveryBadgeStore{Store: s.comments, waiting: make(chan struct{})}
	s.comments = wrapped
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), recoveryContextKey{}, "request-scope"))
	defer cancel()
	done := make(chan struct{})
	var w *httptest.ResponseRecorder
	var aborted bool
	go func() {
		defer close(done)
		w, aborted = commentAuthorityServe(s.routes(), diagnosticRequest("/admin/wechat/", cookie, ctx))
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-wrapped.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("badge read never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("badge read ignored cancellation")
	}
	if !aborted || w.Body.Len() != 0 || wrapped.ctx.Value(recoveryContextKey{}) != "request-scope" {
		t.Fatal("badge lost request cancellation or values", aborted, w.Code, wrapped.ctx.Value(recoveryContextKey{}))
	}
}

func TestAdminSummaryUsesOnePluginSnapshot(t *testing.T) {
	s, _, _, _, _, owner := newPersistentAdminTestServer(t)
	if err := s.plugins.InstallBundled(plugin.AIProMax); err != nil {
		t.Fatal(err)
	}
	s.comments = &accountAuthorityComments{Store: s.comments, afterSnapshot: func() {
		if err := s.plugins.Uninstall(plugin.AIProMax); err != nil {
			t.Fatal(err)
		}
	}}
	view := s.adminView("plugins", owner, false)
	if view.PluginCount != 1 || len(view.PluginSettings) != 1 || view.PluginSettings[0].Slug != plugin.AIProMax {
		t.Fatal("summary performed a second inconsistent plugin read", view.PluginCount, len(view.PluginSettings))
	}
	next := s.adminView("plugins", owner, false)
	if next.PluginCount != 0 || len(next.PluginSettings) != 0 {
		t.Fatal("removed plugin retained across requests")
	}
}

func TestAdminPluginPageUsesOnePluginSnapshot(t *testing.T) {
	s, _, _, _, cookie, _ := newPersistentAdminTestServer(t)
	if err := s.plugins.InstallBundled(plugin.AIProMax); err != nil {
		t.Fatal(err)
	}
	changed := false
	s.navigation = &navigationAuthorityStore{Store: s.navigation, afterSnapshot: func() {
		changed = true
		if err := s.plugins.Uninstall(plugin.AIProMax); err != nil {
			t.Fatal(err)
		}
	}}
	for _, first := range []bool{true, false} {
		w, aborted := commentAuthorityServe(s.routes(), diagnosticRequest("/admin/plugins/", cookie, t.Context()))
		if !changed || aborted || w.Code != 200 || strings.Contains(w.Body.String(), `data-admin-entry="plugin:ai-pro-max"`) != first {
			t.Fatal("plugin cards and navigation used different snapshots", first, changed, aborted, w.Code)
		}
	}
}
