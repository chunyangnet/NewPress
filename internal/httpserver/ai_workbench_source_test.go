package httpserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aiworkbench"
	"newpress.local/newpress/internal/analytics"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

// A repository can return a previously public snapshot just as a DB query can
// finish while another request withdraws the article.
type workbenchSourceRepository struct {
	content.Repository
	afterRead    func()
	afterHeaders func()
	change       atomic.Int32
	fullReads    atomic.Int32
	headerReads  atomic.Int32
}

func (r *workbenchSourceRepository) GetByID(ctx context.Context, id int64) (content.Post, error) {
	r.fullReads.Add(1)
	p, err := r.Repository.GetByID(ctx, id)
	runAutosaveBoundary(&r.afterRead)
	return p, err
}

func (r *workbenchSourceRepository) ListPublishedPage(ctx context.Context, n, offset int) (content.PublishedPage, error) {
	p, err := r.Repository.ListPublishedPage(ctx, n, offset)
	runAutosaveBoundary(&r.afterRead)
	return p, err
}

func (r *workbenchSourceRepository) SearchPublished(ctx context.Context, q content.PublishedQuery) (content.PublishedPage, error) {
	p, err := r.Repository.SearchPublished(ctx, q)
	runAutosaveBoundary(&r.afterRead)
	return p, err
}

func (r *workbenchSourceRepository) GetPostHeaders(ctx context.Context, ids []int64) (map[int64]content.PostHeader, error) {
	r.headerReads.Add(1)
	values, err := content.ReadPostHeaders(ctx, r.Repository, ids)
	for id, p := range values {
		switch r.change.Load() {
		case 1:
			p.Status = content.StatusDraft
		case 2:
			delete(values, id)
			continue
		case 3:
			p.Version++
		case 4:
			return nil, errors.New("PRIVATE-SOURCE-STORAGE-DETAIL")
		}
		values[id] = p
	}
	runAutosaveBoundary(&r.afterRead)
	runAutosaveBoundary(&r.afterHeaders)
	return values, err
}

type workbenchSourceAnalytics struct{ id int64 }

func (a workbenchSourceAnalytics) Apply(context.Context, analytics.Batch, time.Time) error {
	return nil
}
func (a workbenchSourceAnalytics) Snapshot(ctx context.Context, now time.Time) (analytics.State, error) {
	location, _ := analytics.LocationFromContext(ctx)
	return analytics.State{Days: map[string]analytics.Day{analytics.DateIn(now, location): {Views: 1, Posts: map[int64]int64{a.id: 1}}}}, nil
}

func workbenchSourceFixture(t *testing.T, s *Server) (*workbenchSourceRepository, int64) {
	t.Helper()
	seedWorkbench(t, s)
	p, err := s.repository.Create(t.Context(), content.PostInput{Title: "SOURCE-ARTICLE", Slug: "source-article", Status: content.StatusPublish, ContentSource: "CONFIRMED-SOURCE-BODY"})
	if err != nil {
		t.Fatal(err)
	}
	r := &workbenchSourceRepository{Repository: s.repository}
	s.repository = r
	s.SetAnalytics(analytics.NewCollector(workbenchSourceAnalytics{id: p.ID}))
	return r, p.ID
}

func workbenchSourceInput(task string, id int64) string {
	if task == "summary" {
		return `{"task":"summary","days":7,"post_id":` + jsonNumber(id) + `}`
	}
	return `{"task":"` + task + `","days":7}`
}

func TestAIWorkbenchSourceStalePreview(t *testing.T) {
	for _, task := range []string{"summary", "seo", "planning", "briefing"} {
		for _, change := range []int32{1, 2, 3, 4} {
			t.Run(task+"/"+jsonNumber(int64(change)), func(t *testing.T) {
				t.Parallel()
				s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
				r, id := workbenchSourceFixture(t, s)
				previous, err := s.aiPreviews.Put(actor.UserID, aiworkbench.Preview{Task: "summary", Context: "previous"}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				r.afterRead = func() { r.change.Store(change) }
				w, aborted := commentAuthorityServe(s.routes(), workbenchAuthorityRequest("preview", cookie, actor, t.Context(), workbenchSourceInput(task, id)))
				want := 409
				if change == 4 {
					want = 503
				}
				if aborted || w.Code != want || strings.Contains(w.Body.String(), "SOURCE-ARTICLE") || strings.Contains(w.Body.String(), "PRIVATE-SOURCE") {
					t.Fatal("stale snapshot exposed", aborted, w.Code, w.Body.String())
				}
				if _, err := s.aiPreviews.Take(actor.UserID, previous.Token, time.Now()); err != nil {
					t.Fatal("failed preview replaced prior confirmation", err)
				}
			})
		}
	}
}

func TestAIWorkbenchSourceGenerationBoundaries(t *testing.T) {
	for _, boundary := range []string{"before-request", "before-ready", "after-ready", "upstream", "completion"} {
		for _, change := range []int32{0, 1, 2, 3, 4} {
			t.Run(boundary+"/"+jsonNumber(int64(change)), func(t *testing.T) {
				t.Parallel()
				s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
				r, id := workbenchSourceFixture(t, s)
				preview := workbenchTestPreview(t, s.routes(), cookie, actor.CSRFToken, workbenchSourceInput("summary", id))
				var calls atomic.Int32
				after := func() { r.change.Store(change) }
				s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(req *http.Request) (*http.Response, error) {
					calls.Add(1)
					body, _ := io.ReadAll(req.Body)
					if !strings.Contains(string(body), "CONFIRMED-SOURCE-BODY") {
						t.Error("confirmed source replaced")
					}
					if boundary == "upstream" {
						after()
					}
					reader := &aiGenerationTrackedBody{ReadCloser: io.NopCloser(strings.NewReader(aiFrame(workbenchAuthorityMarker, "stop") + "data: [DONE]\n\n"))}
					if boundary == "completion" {
						reader.afterClose = after
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
				})}}
				w := &aiGenerationBoundaryWriter{ResponseRecorder: httptest.NewRecorder()}
				switch boundary {
				case "before-request":
					after()
				case "before-ready":
					w.deadline = after
				case "after-ready":
					w.flush = func() error { runAutosaveBoundary(&after); return nil }
				}
				aborted := serveAIGenerationBoundary(s.routes(), w, workbenchAuthorityRequest("generate", cookie, actor, t.Context(), workbenchGeneration(preview.Token)))
				waitAIGenerationSlot(t, s, actor.UserID)
				wantCalls := int32(1)
				if change != 0 && (boundary == "before-request" || boundary == "before-ready" || boundary == "after-ready") {
					wantCalls = 0
				}
				if aborted || calls.Load() != wantCalls {
					t.Fatal("source boundary bypassed", aborted, calls.Load(), wantCalls, w.Body.String())
				}
				if (len(s.aiReports.List(actor.UserID, time.Now())) == 1) != (change == 0) {
					t.Fatal("stale report persisted")
				}
				if strings.Contains(w.Body.String(), "event: done") != (change == 0) || strings.Contains(w.Body.String(), "PRIVATE-SOURCE") {
					t.Fatal("unsafe completion", w.Body.String())
				}
				if change != 0 && boundary != "completion" && strings.Contains(w.Body.String(), workbenchAuthorityMarker) {
					t.Fatal("stale model payload escaped")
				}
				if r.fullReads.Load() != 1 {
					t.Fatal("source checks reread article bodies", r.fullReads.Load())
				}
			})
		}
	}
}

func TestAIWorkbenchSourceAnalyticsLease(t *testing.T) {
	for _, boundary := range []string{"collection", "after-preview"} {
		for _, task := range []string{"briefing", "planning", "summary", "costs", "seo"} {
			t.Run(boundary+"/"+task, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
				r, id := workbenchSourceFixture(t, s)
				restart := func() {
					accountAuthorityModule(t, s, "analytics", false)
					accountAuthorityModule(t, s, "analytics", true)
				}
				dependent := task == "briefing" || task == "planning"
				input := workbenchSourceInput(task, id)
				if task == "summary" {
					input = `{"task":"summary","days":7,"material":"manual material"}`
				}
				if boundary == "collection" {
					// Analytics reads full posts, recent-article collection uses lists.
					// Revoke on the analytics read for planning, after taking its lease.
					if task != "planning" {
						r.afterRead = restart
					}
					if task == "planning" {
						s.analytics = analytics.NewCollector(&workbenchAnalyticsBoundary{workbenchSourceAnalytics: workbenchSourceAnalytics{id: id}, after: restart})
					}
					if task == "summary" || task == "costs" {
						restart()
					}
					w, aborted := commentAuthorityServe(s.routes(), workbenchAuthorityRequest("preview", cookie, actor, t.Context(), input))
					want := 200
					if dependent {
						want = 409
					}
					if aborted || w.Code != want {
						t.Fatal("wrong analytics dependency", aborted, w.Code, want, w.Body.String())
					}
					return
				}
				p := workbenchTestPreview(t, s.routes(), cookie, actor.CSRFToken, input)
				restart()
				var calls atomic.Int32
				setAIResponse(s, 200, "text/event-stream", aiFrame("report", "stop")+"data: [DONE]\n\n", func(*http.Request) { calls.Add(1) })
				w, aborted := commentAuthorityServe(s.routes(), workbenchAuthorityRequest("generate", cookie, actor, t.Context(), workbenchGeneration(p.Token)))
				waitAIGenerationSlot(t, s, actor.UserID)
				want := 200
				if dependent {
					want = 409
				}
				if aborted || w.Code != want || (calls.Load() != 0) == dependent {
					t.Fatal("old analytics lease revived", aborted, w.Code, want, calls.Load())
				}
			})
		}
	}
}

func TestAIWorkbenchSourceSilentUpstreamCancellation(t *testing.T) {
	for _, source := range []string{"article", "analytics"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
			r, id := workbenchSourceFixture(t, s)
			task := "summary"
			if source == "analytics" {
				task = "briefing"
			}
			p := workbenchTestPreview(t, s.routes(), cookie, actor.CSRFToken, workbenchSourceInput(task, id))
			entered, done := make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(req *http.Request) (*http.Response, error) {
				close(entered)
				<-req.Context().Done()
				return nil, req.Context().Err()
			})}}
			w := httptest.NewRecorder()
			go func() {
				defer close(done)
				s.routes().ServeHTTP(w, workbenchAuthorityRequest("generate", cookie, actor, ctx, workbenchGeneration(p.Token)))
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("upstream never started")
			}
			if source == "article" {
				r.change.Store(1)
			} else {
				accountAuthorityModule(t, s, "analytics", false)
				accountAuthorityModule(t, s, "analytics", true)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("silent upstream ignored source withdrawal")
			}
			waitAIGenerationSlot(t, s, actor.UserID)
			if !strings.Contains(w.Body.String(), "ai.source_changed") || strings.Contains(w.Body.String(), "event: done") || len(s.aiReports.List(actor.UserID, time.Now())) != 0 {
				t.Fatal("stale silent request not stopped", w.Body.String())
			}
		})
	}
}

type workbenchAnalyticsBoundary struct {
	workbenchSourceAnalytics
	after func()
}

func (a *workbenchAnalyticsBoundary) Snapshot(ctx context.Context, now time.Time) (analytics.State, error) {
	state, err := a.workbenchSourceAnalytics.Snapshot(ctx, now)
	runAutosaveBoundary(&a.after)
	return state, err
}

func TestAIWorkbenchSourceSessionAfterHeaderRead(t *testing.T) {
	for _, change := range []string{"logout", "permission", "outage"} {
		for _, phase := range []string{"preview", "generate"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				manager, users, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilitySystemRead})
				fault := &aiGenerationFaultStore{SessionStore: sessions.SessionStore}
				sessions.SessionStore = fault
				r, id := workbenchSourceFixture(t, s)
				input := workbenchSourceInput("summary", id)
				if phase == "generate" {
					p := workbenchTestPreview(t, s.routes(), cookie, actor.CSRFToken, input)
					input = workbenchGeneration(p.Token)
				}
				r.afterHeaders = func() { aiUsageChange(t, s, manager, users, fault, cookie, change, "ai", func() {}) }
				var calls atomic.Int32
				setAIResponse(s, 200, "text/event-stream", aiFrame("report", "stop")+"data: [DONE]\n\n", func(*http.Request) { calls.Add(1) })
				w, aborted := commentAuthorityServe(s.routes(), workbenchAuthorityRequest(phase, cookie, actor, t.Context(), input))
				waitAIGenerationSlot(t, s, actor.UserID)
				want := map[string]int{"logout": 401, "permission": 403, "outage": 503}[change]
				if aborted || w.Code != want || calls.Load() != 0 || strings.Contains(w.Body.String(), "SOURCE-ARTICLE") || strings.Contains(w.Body.String(), "PRIVATE-SESSION") {
					t.Fatal("session not checked after blocked source", aborted, w.Code, want)
				}
			})
		}
	}
}

func TestAIWorkbenchSourceFinalPreviewAndCatalogOutput(t *testing.T) {
	for _, action := range []string{"preview", "articles"} {
		t.Run(action, func(t *testing.T) {
			s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
			r, id := workbenchSourceFixture(t, s)
			w := httptest.NewRecorder()
			boundary := &aiEditorAuthorityHeaderWriter{ResponseWriter: w, after: func() { r.change.Store(1) }}
			aborted := serveAIGenerationBoundary(s.routes(), boundary, workbenchAuthorityRequest(action, cookie, actor, t.Context(), workbenchSourceInput("summary", id)))
			if !aborted || w.Body.Len() != 0 {
				t.Fatal("source changed at output boundary", aborted, w.Body.String())
			}
		})
	}
}

func TestAIWorkbenchSourceRealRepositoryFallbackAndHistory(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "fallback"}[fallback], func(t *testing.T) {
			s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
			r, id := workbenchSourceFixture(t, s)
			if fallback {
				s.repository = struct{ content.Repository }{r.Repository}
			} else {
				s.repository = r.Repository
			}
			setAIResponse(s, 200, "text/event-stream", aiFrame("valid report", "stop")+"data: [DONE]\n\n", nil)
			p := workbenchTestPreview(t, s.routes(), cookie, actor.CSRFToken, workbenchSourceInput("summary", id))
			w, _ := commentAuthorityServe(s.routes(), workbenchAuthorityRequest("generate", cookie, actor, t.Context(), workbenchGeneration(p.Token)))
			waitAIGenerationSlot(t, s, actor.UserID)
			if !strings.Contains(w.Body.String(), "event: done") {
				t.Fatal("valid source rejected", w.Body.String())
			}
			history := s.aiReports.List(actor.UserID, time.Now())
			if len(history) != 1 {
				t.Fatal("report not saved")
			}
			p = workbenchTestPreview(t, s.routes(), cookie, actor.CSRFToken, workbenchSourceInput("summary", id))
			post, _ := s.repository.GetByID(t.Context(), id)
			if _, err := s.repository.ChangeStatus(t.Context(), id, content.Mutation{ExpectedVersion: post.Version, Actor: "withdraw"}, content.StatusDraft); err != nil {
				t.Fatal(err)
			}
			w, _ = commentAuthorityServe(s.routes(), workbenchAuthorityRequest("generate", cookie, actor, t.Context(), workbenchGeneration(p.Token)))
			if w.Code != 409 {
				t.Fatal("withdrawn source accepted", w.Code)
			}
			w, _ = commentAuthorityServe(s.routes(), workbenchAuthorityRequest("reports/"+history[0].ID, cookie, actor, t.Context(), ""))
			if w.Code != 200 || !strings.Contains(w.Body.String(), "valid report") {
				t.Fatal("historical report lost", w.Code)
			}
		})
	}
}
