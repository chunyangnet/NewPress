package httpserver

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func TestFeatureStatusAuthorityObjectFinalOutput(t *testing.T) {
	for _, feature := range []string{"article-comments", "ai-related"} {
		for _, change := range []string{"owner", "deleted", "storage"} {
			t.Run(feature+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, repository, _, owner := newPersistentAdminTestServer(t)
				_, _, _, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn})
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "feature-last", "Private feature")
				wrapped := &featureAuthorityRepository{Repository: repository}
				s.repository = wrapped
				recorder := httptest.NewRecorder()
				writer := &aiEditorAuthorityHeaderWriter{ResponseWriter: recorder, after: func() {
					switch change {
					case "owner":
						wrapped.author.Store(owner.UserID)
					case "deleted":
						wrapped.missing.Store(true)
					case "storage":
						wrapped.fail.Store(true)
					}
				}}
				aborted := serveAIGenerationBoundary(s.routes(), writer, featureAuthorityRequest(cookie, t.Context(), fmt.Sprintf("%s&object=%d", feature, post.ID)))
				if !aborted || recorder.Body.Len() != 0 {
					t.Fatal("object status sent after final source change", aborted, recorder.Body.Len())
				}
			})
		}
	}
}

// A legacy repository lacks the optional lightweight authority projection.
type featureAuthorityLegacyRepository struct{ content.Repository }

func TestFeatureStatusAuthorityLegacyRepository(t *testing.T) {
	for _, feature := range []string{"article-comments", "ai-related"} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/changed=%t", feature, changed), func(t *testing.T) {
				t.Parallel()
				s, _, _, repository, _, owner := newPersistentAdminTestServer(t)
				_, _, _, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn})
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "feature-legacy", "Legacy feature")
				wrapped := &featureAuthorityRepository{Repository: repository}
				if changed {
					wrapped.after = func() { wrapped.author.Store(owner.UserID) }
				}
				s.repository = &featureAuthorityLegacyRepository{Repository: wrapped}
				w, aborted := commentAuthorityServe(s.routes(), featureAuthorityRequest(cookie, t.Context(), fmt.Sprintf("%s&object=%d", feature, post.ID)))
				want := 200
				if changed {
					want = 404
				}
				if aborted || w.Code != want || wrapped.states.Load() != 0 {
					t.Fatal("legacy authority fallback failed", aborted, w.Code, want, wrapped.states.Load())
				}
			})
		}
	}
}

func TestFeatureStatusAuthorityInitialStorageFailure(t *testing.T) {
	for _, feature := range []string{"article-comments", "ai-related"} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/missing=%t", feature, missing), func(t *testing.T) {
				t.Parallel()
				s, _, _, repository, cookie, _ := newPersistentAdminTestServer(t)
				wrapped := &featureAuthorityRepository{Repository: repository}
				wrapped.fail.Store(!missing)
				wrapped.missing.Store(missing)
				s.repository = wrapped
				w, aborted := commentAuthorityServe(s.routes(), featureAuthorityRequest(cookie, t.Context(), feature+"&object=1"))
				want := 503
				if missing {
					want = 404
				}
				if aborted || w.Code != want || strings.Contains(w.Body.String(), "PRIVATE-") || strings.Contains(w.Body.String(), `"checked_at"`) {
					t.Fatal("initial source failure returned diagnostics", aborted, w.Code, want)
				}
			})
		}
	}
}

type featureStatusDeadlineRepository struct {
	content.Repository
	seen chan time.Duration
}

func (r *featureStatusDeadlineRepository) GetByID(ctx context.Context, id int64) (content.Post, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		r.seen <- 0
		return content.Post{}, context.DeadlineExceeded
	}
	r.seen <- time.Until(deadline)
	<-ctx.Done()
	return content.Post{}, ctx.Err()
}

func TestFeatureStatusAuthorityObjectReadDeadline(t *testing.T) {
	for _, feature := range []string{"article-comments", "ai-related"} {
		t.Run(feature, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, cookie, _ := newPersistentAdminTestServer(t)
			wrapped := &featureStatusDeadlineRepository{Repository: repository, seen: make(chan time.Duration, 1)}
			s.repository = wrapped
			w, aborted := commentAuthorityServe(s.routes(), featureAuthorityRequest(cookie, t.Context(), feature+"&object=1"))
			budget := <-wrapped.seen
			if budget <= 0 || budget > 5*time.Second || aborted || w.Code != 503 || strings.Contains(w.Body.String(), `"checked_at"`) {
				t.Fatal("initial object read lacks budget or returned partial status", budget, aborted, w.Code)
			}
		})
	}
}

func TestFeatureStatusAuthorityStateReadRevocation(t *testing.T) {
	for _, feature := range []string{"article-comments", "ai-related"} {
		for _, change := range []string{"logout", "permission", "outage", "cancel"} {
			t.Run(feature+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "feature-state", "Feature state")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				changed := false
				wrapped := &featureAuthorityRepository{Repository: repository}
				wrapped.afterState = func(n int32) {
					boundary := int32(1)
					if feature == "ai-related" {
						boundary = 2
					}
					if n == boundary {
						changed = true
						aiUsageChange(t, s, manager, users, fault, cookie, change, "", cancel)
					}
				}
				s.repository = wrapped
				w, aborted := commentAuthorityServe(s.routes(), featureAuthorityRequest(cookie, ctx, fmt.Sprintf("%s&object=%d", feature, post.ID)))
				if !changed {
					t.Fatal("source validation did not use state reader")
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancel during validation produced output", aborted, w.Code)
					}
					return
				}
				want := map[string]int{"logout": 401, "permission": 403, "outage": 503}[change]
				if aborted || w.Code != want || strings.Contains(w.Body.String(), `"checked_at"`) {
					t.Fatal("session not refreshed after source validation", aborted, w.Code, want)
				}
			})
		}
	}
}
