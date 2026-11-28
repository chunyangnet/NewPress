package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
)

const aiGenerationAuthorityText = "PRIVATE-AI-GENERATION-TEXT"

func aiGenerationAuthorityRequest(cookie *http.Cookie, actor auth.Principal, ctx context.Context, input string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/admin/ai/generate", strings.NewReader(input)).WithContext(ctx)
	r.AddCookie(cookie)
	for key, value := range authenticatedAPIHeaders(actor) {
		r.Header.Set(key, value)
	}
	return r
}

func waitAIGenerationSlot(t *testing.T, s *Server, user int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.aiState.mu.Lock()
		active := s.aiState.active[user]
		s.aiState.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("generation worker retained its slot")
}

func TestAIGenerationAuthorityPreparation(t *testing.T) {
	for _, boundary := range []string{"body", "template", "categories"} {
		for _, change := range []string{"logout", "same-account-fallback", "permission", "module", "module-restarted", "cancel", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, manager, repository, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "generation-author", "generation-author@example.test", "Author", "author")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				changed := false
				after := func() {
					changed = true
					switch change {
					case "cancel":
						cancel()
					case "module", "module-restarted":
						accountAuthorityModule(t, s, "ai", false)
						if change == "module-restarted" {
							accountAuthorityModule(t, s, "ai", true)
						}
					case "same-account-fallback":
						autosaveAuthorityRevoke(t, manager, owner, user, cookie, "logout")
					default:
						autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					}
				}
				input := aiGenerationJSON()
				if boundary == "template" {
					store, item, _ := templateAuthorityFixture(t, s)
					var body map[string]any
					if err := json.Unmarshal([]byte(input), &body); err != nil {
						t.Fatal(err)
					}
					body["prompt_id"], body["template_version"] = "team:"+item.ID, item.Version
					raw, _ := json.Marshal(body)
					input, store.afterRead = string(raw), after
				}
				if boundary == "categories" {
					s.repository = &editorAuthorityRepository{Repository: repository, afterTerms: after}
				}
				var calls atomic.Int32
				setAIResponse(s, 200, "text/event-stream", aiFrame(aiGenerationAuthorityText, "stop")+"data: [DONE]\n\n", func(*http.Request) { calls.Add(1) })
				r := aiGenerationAuthorityRequest(cookie, actor, ctx, input)
				var fallback string
				if change == "same-account-fallback" {
					var err error
					fallback, _, err = manager.AuthenticateContext(t.Context(), "generation-fallback", user.Username, "Account password for "+user.Username+" 2026")
					if err != nil {
						t.Fatal(err)
					}
					r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: fallback})
				}
				if boundary == "body" {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
				}
				w, aborted := commentAuthorityServe(s.routes(), r)
				waitAIGenerationSlot(t, s, actor.UserID)
				wantCalls := int32(0)
				if change == "allowed" {
					wantCalls = 1
				}
				if !changed || calls.Load() != wantCalls {
					t.Errorf("preparation reached model: changed=%v calls=%d/%d", changed, calls.Load(), wantCalls)
				}
				if fallback != "" {
					if p, err := manager.ResolveSession(t.Context(), fallback); err != nil || p.UserID != actor.UserID {
						t.Fatal("fallback was not independently valid", err)
					}
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled preparation returned output", aborted, w.Code)
					}
					return
				}
				want := 403
				if change == "logout" || change == "same-account-fallback" {
					want = 401
				} else if strings.HasPrefix(change, "module") {
					want = 409
				} else if change == "allowed" {
					want = 200
				}
				if aborted || w.Code != want || strings.Contains(w.Body.String(), aiGenerationAuthorityText) != (change == "allowed") || strings.Contains(w.Body.String(), "synthetic-test-key") {
					t.Fatal("preparation authority response", aborted, w.Code, want)
				}
			})
		}
	}
}

type aiGenerationTrackedBody struct {
	io.ReadCloser
	closed     atomic.Bool
	afterClose func()
}

func (b *aiGenerationTrackedBody) Close() error {
	err := b.ReadCloser.Close()
	if b.afterClose != nil {
		b.afterClose()
	}
	b.closed.Store(true)
	return err
}

func TestAIGenerationAuthorityModelBoundaries(t *testing.T) {
	for _, boundary := range []string{"upstream", "completion"} {
		for _, change := range []string{"logout", "permission", "module", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, manager, _, _, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "generation-editor", "generation-editor@example.test", "Editor", "editor")
				var changed atomic.Bool
				after := func() {
					if change == "module" {
						accountAuthorityModule(t, s, "ai", false)
					} else {
						autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
					}
					changed.Store(true)
				}
				body := &aiGenerationTrackedBody{ReadCloser: io.NopCloser(strings.NewReader(aiFrame(aiGenerationAuthorityText, "stop") + "data: [DONE]\n\n"))}
				if boundary == "completion" {
					body.afterClose = after
				}
				s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
					if boundary == "upstream" {
						after()
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
				})}}
				w, aborted := commentAuthorityServe(s.routes(), aiGenerationAuthorityRequest(cookie, actor, t.Context(), aiGenerationJSON()))
				waitAIGenerationSlot(t, s, actor.UserID)
				if !changed.Load() || !body.closed.Load() || aborted || w.Code != 200 {
					t.Fatal("model boundary not completed", changed.Load(), body.closed.Load(), aborted, w.Code)
				}
				if change == "allowed" {
					if !strings.Contains(w.Body.String(), "event: done") || !strings.Contains(w.Body.String(), aiGenerationAuthorityText) {
						t.Fatal("allowed result missing")
					}
				} else if strings.Contains(w.Body.String(), "event: done") || !strings.Contains(w.Body.String(), "event: error") || boundary == "upstream" && strings.Contains(w.Body.String(), aiGenerationAuthorityText) {
					t.Fatal("revoked result exposed", w.Body.String())
				}
			})
		}
	}
}
