package httpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/plugin"
)

const aiEditorAuthorityOutput = "PRIVATE-AI-EDITOR-RESULT"

func aiEditorAuthorityInput(tool string) (string, string) {
	if tool == "metadata" {
		return `{"title":"Article","body":"PRIVATE-EDITOR-INPUT","language":"zh-CN","model":"writer:writer-model","fields":["excerpt"]}`, `{"excerpt":"` + aiEditorAuthorityOutput + `"}`
	}
	return `{"text":"PRIVATE-EDITOR-INPUT","language":"zh-CN","mode":"polish","model":"writer:writer-model"}`, aiEditorAuthorityOutput
}

func aiEditorAuthorityRequest(tool string, cookie *http.Cookie, p auth.Principal, ctx context.Context) *http.Request {
	body, _ := aiEditorAuthorityInput(tool)
	r := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/admin/plugins/ai-pro-max/"+tool, strings.NewReader(body)).WithContext(ctx)
	r.AddCookie(cookie)
	for name, value := range authenticatedAPIHeaders(p) {
		r.Header.Set(name, value)
	}
	return r
}

func TestAIEditorAuthorityBoundaries(t *testing.T) {
	for _, tool := range []string{"metadata", "paragraph"} {
		for _, boundary := range []string{"body", "categories", "upstream", "completion"} {
			if tool == "paragraph" && boundary == "categories" {
				continue
			}
			for _, change := range []string{"logout", "logout-with-fallback", "permission", "cancel", "module", "plugin-replaced", "allowed"} {
				t.Run(tool+"/"+boundary+"/"+change, func(t *testing.T) {
					t.Parallel()
					s, _, manager, repository, fallback, owner := newPersistentAdminTestServer(t)
					enableWritingFixture(t, s)
					user, cookie, actor := createAndLoginTestUser(t, manager, owner, "ai-tool-editor", "ai-tool-editor@example.test", "Editor", "editor")
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					changed, calls := false, 0
					after := func() {
						changed = true
						switch change {
						case "cancel":
							cancel()
						case "module":
							accountAuthorityModule(t, s, "ai", false)
						case "plugin-replaced":
							for _, enabled := range []bool{false, true} {
								if err := s.plugins.SetEnabled(plugin.AIProMax, enabled); err != nil {
									t.Fatal(err)
								}
							}
						default:
							autosaveAuthorityRevoke(t, manager, owner, user, cookie, change)
						}
					}
					wrapped := &editorAuthorityRepository{Repository: repository}
					if boundary == "categories" {
						wrapped.afterTerms = after
					}
					s.repository = wrapped
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
					r := aiEditorAuthorityRequest(tool, cookie, actor, ctx)
					if change == "logout-with-fallback" {
						r.AddCookie(fallback)
					}
					if boundary == "body" {
						r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
					}
					w, aborted := commentAuthorityServe(s.routes(), r)
					wantCalls := 0
					if boundary == "upstream" || boundary == "completion" || change == "allowed" {
						wantCalls = 1
					}
					if !changed || calls != wantCalls || wrapped.mutations != 0 || calls > 0 && !body.closed {
						t.Errorf("boundary side effects: changed=%v calls=%d/%d mutations=%d closed=%v", changed, calls, wantCalls, wrapped.mutations, body.closed)
					}
					if !s.aiState.acquire(actor.UserID) {
						t.Fatal("generation slot was not released")
					}
					s.aiState.release(actor.UserID)
					if change == "cancel" {
						if !aborted || w.Body.Len() > 0 {
							t.Fatal("cancelled tool returned output", aborted, w.Code)
						}
						return
					}
					want := 403
					if strings.HasPrefix(change, "logout") {
						want = 401
					} else if change == "module" || change == "plugin-replaced" {
						want = 409
					} else if change == "allowed" {
						want = 200
					}
					if aborted || w.Code != want || strings.Contains(w.Body.String(), aiEditorAuthorityOutput) != (change == "allowed") || strings.Contains(w.Body.String(), "fixture-only-key") {
						t.Fatal("tool authority response", aborted, w.Code, want)
					}
				})
			}
		}
	}
}
