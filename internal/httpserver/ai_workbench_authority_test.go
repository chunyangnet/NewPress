package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aiworkbench"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

const workbenchAuthorityMarker = "PRIVATE-WORKBENCH-RESULT"

type workbenchAuthorityRepository struct {
	content.Repository
	after func()
}

func (r *workbenchAuthorityRepository) SearchPublished(ctx context.Context, q content.PublishedQuery) (content.PublishedPage, error) {
	v, err := r.Repository.SearchPublished(ctx, q)
	runAutosaveBoundary(&r.after)
	return v, err
}

func (r *workbenchAuthorityRepository) ListPublishedPage(ctx context.Context, limit, offset int) (content.PublishedPage, error) {
	v, err := r.Repository.ListPublishedPage(ctx, limit, offset)
	runAutosaveBoundary(&r.after)
	return v, err
}

func workbenchAuthorityRequest(action string, cookie *http.Cookie, actor auth.Principal, ctx context.Context, input string) *http.Request {
	method := "GET"
	if action == "preview" || action == "generate" {
		method = "POST"
	} else if strings.HasPrefix(action, "delete/") {
		method, action = "DELETE", "reports/"+strings.TrimPrefix(action, "delete/")
	}
	if action == "page" {
		r := httptest.NewRequest("GET", "https://example.test/admin/ai/", nil).WithContext(ctx)
		r.AddCookie(cookie)
		return r
	}
	if action != "" {
		action = "/" + action
	}
	return aiAdminAuthorityRequest(method, "workbench"+action, cookie, actor, ctx, input)
}

func workbenchAuthorityReport(t *testing.T, s *Server, owner int64) aiworkbench.Report {
	t.Helper()
	r := aiworkbench.Report{ID: aiworkbench.NewID(), Owner: owner, Task: "summary", Title: workbenchAuthorityMarker, CreatedAt: time.Now(), Model: "report-model", Content: workbenchAuthorityMarker, Source: &aiworkbench.Preview{Task: "summary", Context: workbenchAuthorityMarker}}
	if err := s.aiReports.Save(r, time.Now()); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAIWorkbenchAuthorityPreparation(t *testing.T) {
	for _, boundary := range []string{"page-navigation", "page-summary", "articles", "preview-body", "preview-costs", "preview-articles", "generate-body"} {
		for _, change := range []string{"logout", "fallback", "permission", "module-restarted", "outage", "cancel", "allowed"} {
			t.Run(boundary+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				seedWorkbench(t, s)
				previous, err := s.aiPreviews.Put(actor.UserID, aiworkbench.Preview{Task: "summary", Context: workbenchAuthorityMarker}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var changed atomic.Bool
				after := func() {
					changed.Store(true)
					aiUsageChange(t, s, manager, users, fault, cookie, change, "ai", cancel)
				}
				action, input := "preview", `{"task":"summary","days":7,"material":"`+workbenchAuthorityMarker+`"}`
				switch boundary {
				case "page-navigation":
					action = "page"
					s.navigation = &navigationAuthorityStore{Store: s.navigation, afterSnapshot: after}
				case "page-summary":
					action = "page"
					s.comments = &accountAuthorityComments{Store: s.comments, afterSnapshot: after}
				case "articles":
					action = "articles"
					s.repository = &workbenchAuthorityRepository{Repository: repository, after: after}
				case "preview-articles":
					input = `{"task":"seo","days":7}`
					s.repository = &workbenchAuthorityRepository{Repository: repository, after: after}
				case "preview-costs":
					input = `{"task":"costs","days":7}`
					aiUsageTestTelemetry(t, s, after)
				case "generate-body":
					action, input = "generate", workbenchGeneration(previous.Token)
				}
				r := workbenchAuthorityRequest(action, cookie, actor, ctx, input)
				if change == "fallback" {
					token, _, err := manager.AuthenticateContext(t.Context(), "workbench-fallback", actor.Username, "Account password for "+actor.Username+" 2026")
					if err != nil {
						t.Fatal(err)
					}
					r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
				}
				if strings.HasSuffix(boundary, "body") {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
				}
				var calls atomic.Int32
				setAIResponse(s, 200, "text/event-stream", aiFrame(workbenchAuthorityMarker, "stop")+"data: [DONE]\n\n", func(*http.Request) { calls.Add(1) })
				w, aborted := commentAuthorityServe(s.routes(), r)
				waitAIGenerationSlot(t, s, actor.UserID)
				wantCalls := int32(0)
				if action == "generate" && change == "allowed" {
					wantCalls = 1
				}
				if !changed.Load() || calls.Load() != wantCalls {
					t.Error("preparation boundary bypassed", changed.Load(), calls.Load(), wantCalls)
				}
				if change != "allowed" {
					if _, err := s.aiPreviews.Take(actor.UserID, previous.Token, time.Now()); err != nil {
						t.Error("denied request consumed/replaced previous preview", err)
					}
					if len(s.aiReports.List(actor.UserID, time.Now())) != 0 {
						t.Error("denied generation saved report")
					}
				}
				if change == "cancel" {
					if !aborted || w.Body.Len() != 0 {
						t.Fatal("cancelled workbench request returned data", aborted, w.Code)
					}
					return
				}
				want := map[string]int{"logout": 401, "fallback": 401, "permission": 403, "module-restarted": 409, "outage": 503, "allowed": 200}[change]
				if action == "page" && want == 401 {
					want = 303
				}
				if aborted || w.Code != want || change != "allowed" && (strings.Contains(w.Body.String(), workbenchAuthorityMarker) || strings.Contains(w.Body.String(), "data-ai-workbench")) || strings.Contains(w.Body.String(), "PRIVATE-SESSION-DETAIL") {
					t.Fatal("workbench authority response", aborted, w.Code, want)
				}
			})
		}
	}
}

func TestAIWorkbenchAuthorityFinalOutput(t *testing.T) {
	for _, action := range []string{"page", "", "articles", "preview", "reports", "report", "delete"} {
		for _, change := range []string{"logout", "module-restarted"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, manager, _, cookie, actor := newPersistentAdminTestServer(t)
				seedWorkbench(t, s)
				report := workbenchAuthorityReport(t, s, actor.UserID)
				route := action
				if action == "report" {
					route = "reports/" + report.ID
				} else if action == "delete" {
					route = "delete/" + report.ID
				}
				changed := false
				recorder := httptest.NewRecorder()
				w := &aiEditorAuthorityHeaderWriter{ResponseWriter: recorder, after: func() {
					changed = true
					if change == "logout" {
						if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
							t.Fatal(err)
						}
					} else {
						accountAuthorityModule(t, s, "ai", false)
						accountAuthorityModule(t, s, "ai", true)
					}
				}}
				aborted := serveAIGenerationBoundary(s.routes(), w, workbenchAuthorityRequest(route, cookie, actor, t.Context(), `{"task":"summary","days":7,"material":"text"}`))
				if !changed || !aborted || recorder.Body.Len() != 0 {
					t.Fatal("final output disclosed workbench data", changed, aborted, recorder.Body.Len())
				}
				if action == "delete" && len(s.aiReports.List(actor.UserID, time.Now())) != 0 {
					t.Fatal("output revocation rolled back already committed deletion")
				}
			})
		}
	}
}

func TestAIWorkbenchAuthoritySystemReader(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	_, _, _, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
	seedWorkbench(t, s)
	accountAuthorityModule(t, s, "analytics", false)
	setAIResponse(s, 200, "text/event-stream", aiFrame(workbenchAuthorityMarker, "stop")+"data: [DONE]\n\n", nil)
	preview := workbenchTestPreview(t, s.routes(), cookie, actor.CSRFToken, `{"task":"summary","days":7,"material":"text"}`)
	w, aborted := commentAuthorityServe(s.routes(), workbenchAuthorityRequest("generate", cookie, actor, t.Context(), workbenchGeneration(preview.Token)))
	waitAIGenerationSlot(t, s, actor.UserID)
	if aborted || w.Code != 200 || !strings.Contains(w.Body.String(), "event: done") || len(s.aiReports.List(actor.UserID, time.Now())) != 1 {
		t.Fatal("system reader lost workbench access", aborted, w.Code, w.Body.String())
	}
	for _, action := range []string{"", "page", "articles", "reports"} {
		w, aborted = commentAuthorityServe(s.routes(), workbenchAuthorityRequest(action, cookie, actor, t.Context(), ""))
		if aborted || w.Code != 200 {
			t.Fatal(action, aborted, w.Code)
		}
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
}

func TestAIWorkbenchAuthorityRequestProof(t *testing.T) {
	for _, action := range []string{"preview", "generate"} {
		for _, change := range []string{"origin", "csrf", "duplicate-csrf"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
				seedWorkbench(t, s)
				previous, err := s.aiPreviews.Put(actor.UserID, aiworkbench.Preview{Task: "summary", Context: "original"}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				input := `{"task":"summary","days":7,"material":"replacement"}`
				if action == "generate" {
					input = workbenchGeneration(previous.Token)
				}
				r := workbenchAuthorityRequest(action, cookie, actor, t.Context(), input)
				r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: func() {
					switch change {
					case "origin":
						r.Header.Set("Origin", "https://unrelated.test")
					case "csrf":
						r.Header.Set("X-CSRF-Token", "stale-token")
					case "duplicate-csrf":
						r.Header.Add("X-CSRF-Token", actor.CSRFToken)
					}
				}}
				var calls atomic.Int32
				setAIResponse(s, 200, "text/event-stream", aiFrame(workbenchAuthorityMarker, "stop")+"data: [DONE]\n\n", func(*http.Request) { calls.Add(1) })
				w, aborted := commentAuthorityServe(s.routes(), r)
				waitAIGenerationSlot(t, s, actor.UserID)
				if aborted || w.Code != 403 || calls.Load() != 0 || len(s.aiReports.List(actor.UserID, time.Now())) != 0 {
					t.Fatal("stale request proof used workbench", aborted, w.Code, calls.Load())
				}
				if got, err := s.aiPreviews.Take(actor.UserID, previous.Token, time.Now()); err != nil || got != previous {
					t.Fatal("denied request consumed/replaced token", err)
				}
			})
		}
	}
}
