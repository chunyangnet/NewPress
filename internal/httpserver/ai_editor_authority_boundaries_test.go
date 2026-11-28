package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
)

func TestAIEditorAuthoritySessionOutage(t *testing.T) {
	for _, tool := range []string{"metadata", "paragraph"} {
		for _, boundary := range []string{"body", "categories", "upstream", "completion"} {
			if tool == "paragraph" && boundary == "categories" {
				continue
			}
			t.Run(tool+"/"+boundary, func(t *testing.T) {
				t.Parallel()
				s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
				enableWritingFixture(t, s)
				_, _, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, nil)
				changed, calls := false, 0
				after := func() { changed = true; sessions.failAt = sessions.lookups.Load() + 1 }
				if boundary == "categories" {
					s.repository = &editorAuthorityRepository{Repository: repository, afterTerms: after}
				}
				_, output := aiEditorAuthorityInput(tool)
				body := &aiCommentAuthorityResponseBody{ReadCloser: io.NopCloser(strings.NewReader(aiFrame(output, "stop") + "data: [DONE]\n\n"))}
				if boundary == "completion" {
					body.afterClose = after
				}
				s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
					calls++
					if boundary == "upstream" {
						after()
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
				})}}
				r := aiEditorAuthorityRequest(tool, cookie, actor, t.Context())
				if boundary == "body" {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
				}
				w, aborted := commentAuthorityServe(s.routes(), r)
				wantCalls := 0
				if boundary == "upstream" || boundary == "completion" {
					wantCalls = 1
				}
				if !changed || aborted || w.Code != 503 || calls != wantCalls || w.Header().Get("Retry-After") != "5" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("WWW-Authenticate") != "" || strings.Contains(w.Body.String(), "PRIVATE-") {
					t.Fatal("session outage response", changed, aborted, calls, w.Code, w.Header())
				}
			})
		}
	}
}

func TestAIEditorAuthorityStopsStream(t *testing.T) {
	for _, tool := range []string{"metadata", "paragraph"} {
		for _, kind := range []string{"text", "thinking", "usage", "later-event"} {
			t.Run(tool+"/"+kind, func(t *testing.T) {
				t.Parallel()
				s, _, manager, _, cookie, actor := newPersistentAdminTestServer(t)
				enableWritingFixture(t, s)
				frame := aiFrame("Partial text", "")
				if kind == "thinking" {
					frame = "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"Thinking\"}}]}\n\n"
				} else if kind == "usage" {
					frame = "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6}}\n\n"
				}
				_, output := aiEditorAuthorityInput(tool)
				frames := &aiEditorAuthorityStream{frames: []string{frame, frame, aiFrame(output, "stop") + "data: [DONE]\n\n"}}
				wantReads := 1
				if kind == "later-event" {
					wantReads = 2
				}
				frames.beforeRead = func(reads int) {
					if reads != wantReads {
						return
					}
					if kind == "later-event" {
						time.Sleep(1100 * time.Millisecond)
					}
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Fatal(err)
					}
				}
				body := &aiCommentAuthorityResponseBody{ReadCloser: io.NopCloser(frames)}
				s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
				})}}
				w, aborted := commentAuthorityServe(s.routes(), aiEditorAuthorityRequest(tool, cookie, actor, t.Context()))
				if aborted || w.Code != 401 || frames.reads != wantReads || !body.closed || strings.Contains(w.Body.String(), "PRIVATE-") {
					t.Fatal("revoked stream continued", aborted, w.Code, frames.reads, body.closed)
				}
				if !s.aiState.acquire(actor.UserID) {
					t.Fatal("stream leaked generation slot")
				}
				s.aiState.release(actor.UserID)
			})
		}
	}
}

type aiEditorAuthorityStream struct {
	frames     []string
	reads      int
	beforeRead func(int)
}

func (s *aiEditorAuthorityStream) Read(p []byte) (int, error) {
	s.reads++
	if s.beforeRead != nil {
		s.beforeRead(s.reads)
	}
	if len(s.frames) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.frames[0])
	s.frames[0] = s.frames[0][n:]
	if s.frames[0] == "" {
		s.frames = s.frames[1:]
	}
	return n, nil
}

func TestAIEditorAuthorityBoundedStreamChecks(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	enableWritingFixture(t, s)
	_, _, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, nil)
	setAIResponse(s, 200, "text/event-stream", strings.Repeat(aiFrame("x", ""), 200)+aiFrame("end", "stop")+"data: [DONE]\n\n", nil)
	before := sessions.lookups.Load()
	w, aborted := commentAuthorityServe(s.routes(), aiEditorAuthorityRequest("paragraph", cookie, actor, t.Context()))
	checks := sessions.lookups.Load() - before
	if aborted || w.Code != 200 || checks < 5 || checks > 25 || !strings.Contains(w.Body.String(), strings.Repeat("x", 200)+"end") {
		t.Fatal("stream checks not bounded or output changed", aborted, w.Code, checks)
	}
}

func TestAIEditorAuthorityFallback(t *testing.T) {
	for _, tool := range []string{"metadata", "paragraph"} {
		for _, revoke := range []bool{false, true} {
			name := "allowed"
			if revoke {
				name = "logout"
			}
			t.Run(tool+"/"+name, func(t *testing.T) {
				t.Parallel()
				s, _, manager, _, cookie, actor := newPersistentAdminTestServer(t)
				enableWritingFixture(t, s)
				settings := s.aiSettings.Snapshot()
				settings.Providers = append(settings.Providers, aieditor.Provider{ID: "fallback", Name: "Fallback", BaseURL: "https://fallback.example.test/v1", APIKey: "fixture-only-key", Models: []string{"fallback-model"}, DefaultModel: "fallback-model"})
				if _, err := s.aiSettings.Save(settings); err != nil {
					t.Fatal(err)
				}
				state, err := s.aiJobs.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				rules := s.defaultProRules(state.Rules, settings)
				rules.Fallback, rules.UserID, rules.Author = true, actor.UserID, actor.Username
				if err := s.aiJobs.SaveRules(state.Epoch, rules); err != nil {
					t.Fatal(err)
				}
				_, output := aiEditorAuthorityInput(tool)
				calls := 0
				s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						if revoke {
							if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
								t.Fatal(err)
							}
						}
						return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Temporary failure"}}`))}, nil
					}
					if r.URL.Host != "fallback.example.test" {
						t.Error("wrong fallback provider", r.URL.Host)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(aiFrame(output, "stop") + "data: [DONE]\n\n"))}, nil
				})}}
				w, aborted := commentAuthorityServe(s.routes(), aiEditorAuthorityRequest(tool, cookie, actor, t.Context()))
				want, wantCalls := 200, 2
				if revoke {
					want, wantCalls = 401, 1
				}
				if aborted || w.Code != want || calls != wantCalls || strings.Contains(w.Body.String(), aiEditorAuthorityOutput) == revoke {
					t.Fatal("fallback session boundary", aborted, w.Code, calls)
				}
			})
		}
	}
}

func TestAIEditorAuthorityIndependentCapabilities(t *testing.T) {
	for _, tool := range []string{"metadata", "paragraph"} {
		for _, keep := range []string{"admin", "write-own", "both"} {
			t.Run(tool+"/"+keep, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, _, _ := newPersistentAdminTestServer(t)
				enableWritingFixture(t, s)
				capabilities := []string{auth.CapabilityAdminAccess, auth.CapabilityContentWriteOwn}
				_, users, _, cookie, actor := dashboardAuthorityIdentity(t, s, capabilities)
				// Plugin editor tools do not depend on the separate content API.
				contentAuthorityDisable(t, s, false)
				_, output := aiEditorAuthorityInput(tool)
				setAIResponse(s, 200, "text/event-stream", aiFrame(output, "stop")+"data: [DONE]\n\n", func(*http.Request) {
					if keep == "admin" {
						capabilities = capabilities[:1]
					} else if keep == "write-own" {
						capabilities = capabilities[1:]
					}
					if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: capabilities}}); err != nil {
						t.Fatal(err)
					}
				})
				w, aborted := commentAuthorityServe(s.routes(), aiEditorAuthorityRequest(tool, cookie, actor, t.Context()))
				want := 403
				if keep == "both" {
					want = 200
				}
				if aborted || w.Code != want || strings.Contains(w.Body.String(), aiEditorAuthorityOutput) != (keep == "both") {
					t.Fatal("independent capability or API scope", aborted, w.Code, want)
				}
			})
		}
	}
}

type aiEditorAuthorityHeaderWriter struct {
	http.ResponseWriter
	after func()
}

func (w *aiEditorAuthorityHeaderWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	if status == 200 {
		runAutosaveBoundary(&w.after)
	}
}

func TestAIEditorAuthorityFinalOutput(t *testing.T) {
	for _, tool := range []string{"metadata", "paragraph"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			s, _, manager, _, cookie, actor := newPersistentAdminTestServer(t)
			enableWritingFixture(t, s)
			_, output := aiEditorAuthorityInput(tool)
			setAIResponse(s, 200, "text/event-stream", aiFrame(output, "stop")+"data: [DONE]\n\n", nil)
			recorder := httptest.NewRecorder()
			changed, aborted := false, false
			w := &aiEditorAuthorityHeaderWriter{ResponseWriter: recorder, after: func() {
				changed = true
				if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
					t.Fatal(err)
				}
			}}
			func() {
				defer func() {
					if v := recover(); v == http.ErrAbortHandler {
						aborted = true
					} else if v != nil {
						panic(v)
					}
				}()
				s.routes().ServeHTTP(w, aiEditorAuthorityRequest(tool, cookie, actor, t.Context()))
			}()
			if !changed || !aborted || recorder.Body.Len() != 0 {
				t.Fatal("revoked final output exposed result", changed, aborted, recorder.Body.Len())
			}
		})
	}
}

func TestAIEditorAuthorityRequestProofs(t *testing.T) {
	for _, tool := range []string{"metadata", "paragraph"} {
		for _, change := range []string{"origin", "csrf", "duplicate-csrf"} {
			t.Run(tool+"/"+change, func(t *testing.T) {
				t.Parallel()
				s, _, _, _, cookie, actor := newPersistentAdminTestServer(t)
				enableWritingFixture(t, s)
				_, output := aiEditorAuthorityInput(tool)
				calls := 0
				setAIResponse(s, 200, "text/event-stream", aiFrame(output, "stop")+"data: [DONE]\n\n", func(*http.Request) { calls++ })
				r := aiEditorAuthorityRequest(tool, cookie, actor, t.Context())
				changed := false
				r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: func() {
					changed = true
					switch change {
					case "origin":
						r.Header.Set("Origin", "https://other.example.test")
					case "csrf":
						r.Header.Set("X-CSRF-Token", "invalid")
					case "duplicate-csrf":
						r.Header.Add("X-CSRF-Token", actor.CSRFToken)
					}
				}}
				w, aborted := commentAuthorityServe(s.routes(), r)
				if !changed || aborted || w.Code != 403 || calls != 0 || strings.Contains(w.Body.String(), "PRIVATE-") {
					t.Fatal("request proof not refreshed", changed, aborted, w.Code, calls)
				}
			})
		}
	}
}

func TestAIEditorAuthoritySameAccountFallback(t *testing.T) {
	for _, tool := range []string{"metadata", "paragraph"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			s, _, manager, _, _, owner := newPersistentAdminTestServer(t)
			enableWritingFixture(t, s)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "ai-editor-fallback", "ai-editor-fallback@example.test", "Editor", "editor")
			token, _, err := manager.AuthenticateContext(t.Context(), "ai-editor-fallback-two", user.Username, "Account password for "+user.Username+" 2026")
			if err != nil {
				t.Fatal(err)
			}
			_, output := aiEditorAuthorityInput(tool)
			calls := 0
			setAIResponse(s, 200, "text/event-stream", aiFrame(output, "stop")+"data: [DONE]\n\n", func(*http.Request) { calls++ })
			r := aiEditorAuthorityRequest(tool, cookie, actor, t.Context())
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
			r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: func() {
				if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
					t.Fatal(err)
				}
			}}
			w, aborted := commentAuthorityServe(s.routes(), r)
			if aborted || w.Code != 401 || calls != 0 || strings.Contains(w.Body.String(), aiEditorAuthorityOutput) {
				t.Fatal("same-account fallback took over", aborted, w.Code, calls)
			}
			if p, err := manager.ResolveSession(t.Context(), token); err != nil || p.UserID != actor.UserID {
				t.Fatal("fallback session not independently valid", err)
			}
		})
	}
}

func TestAIEditorAuthorityEnvironmentSession(t *testing.T) {
	for _, tool := range []string{"metadata", "paragraph"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			s, _, _, cookie, actor := newCommentHTTPTestServer(t)
			enableWritingFixture(t, s)
			if actor.UserID != 0 {
				t.Fatal("environment administrator expected")
			}
			_, output := aiEditorAuthorityInput(tool)
			setAIResponse(s, 200, "text/event-stream", aiFrame(output, "stop")+"data: [DONE]\n\n", func(*http.Request) {
				if err := s.auth.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
					t.Fatal(err)
				}
			})
			w, aborted := commentAuthorityServe(s.routes(), aiEditorAuthorityRequest(tool, cookie, actor, t.Context()))
			if aborted || w.Code != 401 || strings.Contains(w.Body.String(), aiEditorAuthorityOutput) {
				t.Fatal("environment session not refreshed", aborted, w.Code)
			}
		})
	}
}
