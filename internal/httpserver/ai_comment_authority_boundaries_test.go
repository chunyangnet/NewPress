package httpserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

type aiCommentAuthorityResponseBody struct {
	io.ReadCloser
	afterClose func()
	closed     bool
}

func (b *aiCommentAuthorityResponseBody) Close() error {
	b.closed = true
	err := b.ReadCloser.Close()
	runAutosaveBoundary(&b.afterClose)
	return err
}

func TestAICommentAuthorityPrivatePermission(t *testing.T) {
	for _, boundary := range []string{"body", "post", "upstream"} {
		for _, public := range []bool{false, true} {
			name := "private"
			if public {
				name = "public"
			}
			t.Run(boundary+"/"+name, func(t *testing.T) {
				t.Parallel()
				s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
				enableWritingFixture(t, s)
				manager, users, _, cookie, actor := dashboardAuthorityIdentity(t, s, []string{auth.CapabilityAdminAccess, auth.CapabilityCommentsModerate, auth.CapabilityContentWriteOwn})
				post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "ai-private-permission", "PRIVATE-AI-PARENT")
				if public {
					var err error
					post, err = repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.StatusPublish)
					if err != nil {
						t.Fatal(err)
					}
				}
				c := commentAuthorityFixture(t, s, post.ID)
				changed := false
				after := func() {
					changed = true
					if err := users.EnsureRoles(t.Context(), []auth.Role{{Slug: "dashboard-role", Name: "Dashboard role", Capabilities: []string{auth.CapabilityAdminAccess, auth.CapabilityCommentsModerate}}}); err != nil {
						t.Fatal(err)
					}
				}
				if boundary == "post" {
					s.repository = &editorAuthorityRepository{Repository: repository, afterPost: after}
				}
				calls := 0
				setAIResponse(s, 200, "text/event-stream", aiFrame(aiCommentAuthorityDraft, "stop")+"data: [DONE]\n\n", func(*http.Request) {
					calls++
					if boundary == "upstream" {
						after()
					}
				})
				w, aborted := aiCommentAuthorityRequest(s, c, cookie, actor, t.Context(), func(r *http.Request) {
					if boundary == "body" {
						r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
					}
				})
				want, wantCalls := 403, 0
				if public {
					want = 200
				}
				if public || boundary == "upstream" {
					wantCalls = 1
				}
				current, err := manager.ResolveSession(t.Context(), cookie.Value)
				if err != nil || !current.Can(auth.CapabilityCommentsModerate) || current.Can(auth.CapabilityContentWriteOwn) {
					t.Fatal("test removed moderation permission", current, err)
				}
				if !changed || aborted || w.Code != want || calls != wantCalls || strings.Contains(w.Body.String(), aiCommentAuthorityDraft) != public {
					t.Fatal("private permission not refreshed", changed, aborted, w.Code, calls, wantCalls)
				}
			})
		}
	}
}

func TestAICommentAuthoritySessionOutage(t *testing.T) {
	for _, boundary := range []string{"body", "headers", "upstream", "completion"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, _, _ := newPersistentAdminTestServer(t)
			enableWritingFixture(t, s)
			_, _, sessions, cookie, actor := dashboardAuthorityIdentity(t, s, nil)
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "ai-comment-outage", "PRIVATE-AI-PARENT")
			c := commentAuthorityFixture(t, s, post.ID)
			changed, calls := false, 0
			after := func() { changed = true; sessions.failAt = sessions.lookups.Load() + 1 }
			if boundary == "headers" {
				s.repository = &accountAuthorityHeaders{Repository: repository, afterHeaders: after}
			}
			body := &aiCommentAuthorityResponseBody{ReadCloser: io.NopCloser(strings.NewReader(aiFrame(aiCommentAuthorityDraft, "stop") + "data: [DONE]\n\n"))}
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
			w, aborted := aiCommentAuthorityRequest(s, c, cookie, actor, t.Context(), func(r *http.Request) {
				if boundary == "body" {
					r.Body = &mediaAuthorityBody{ReadCloser: r.Body, onRead: after}
				}
			})
			wantCalls := 0
			if boundary == "upstream" || boundary == "completion" {
				wantCalls = 1
			}
			if !changed || aborted || calls != wantCalls || w.Code != 503 || w.Header().Get("Retry-After") != "5" || w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "PRIVATE-") || calls > 0 && !body.closed {
				t.Fatal("outage exposed draft or cleared cookie", changed, aborted, calls, w.Code, w.Header())
			}
			if !s.aiState.acquire(actor.UserID) {
				t.Fatal("outage leaked generation slot")
			}
			s.aiState.release(actor.UserID)
		})
	}
}

type aiCommentAuthorityHeaders struct {
	*accountAuthorityHeaders
	err error
}

func (r *aiCommentAuthorityHeaders) GetPostHeaders(ctx context.Context, ids []int64) (map[int64]content.PostHeader, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.accountAuthorityHeaders.GetPostHeaders(ctx, ids)
}

func TestAICommentAuthorityProjectionFailure(t *testing.T) {
	for _, boundary := range []string{"dispatch", "upstream"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			s, _, _, repository, cookie, actor := newPersistentAdminTestServer(t)
			enableWritingFixture(t, s)
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "ai-comment-read-failure", "PRIVATE-AI-PARENT")
			c := commentAuthorityFixture(t, s, post.ID)
			parents := &aiCommentAuthorityHeaders{accountAuthorityHeaders: &accountAuthorityHeaders{Repository: repository}}
			s.repository = parents
			if boundary == "dispatch" {
				parents.err = errors.New("projection unavailable")
			}
			calls := 0
			setAIResponse(s, 200, "text/event-stream", aiFrame(aiCommentAuthorityDraft, "stop")+"data: [DONE]\n\n", func(*http.Request) {
				calls++
				parents.err = errors.New("projection unavailable")
			})
			w, aborted := aiCommentAuthorityRequest(s, c, cookie, actor, t.Context(), nil)
			wantCalls := 0
			if boundary == "upstream" {
				wantCalls = 1
			}
			if aborted || w.Code != 503 || calls != wantCalls || strings.Contains(w.Body.String(), "PRIVATE-") {
				t.Fatal("failed projection exposed draft", aborted, w.Code, calls)
			}
		})
	}
}

func TestAICommentAuthorityUsesLightweightChecks(t *testing.T) {
	s, _, _, repository, cookie, actor := newPersistentAdminTestServer(t)
	enableWritingFixture(t, s)
	post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "ai-comment-projection", "PRIVATE-AI-PARENT")
	c := commentAuthorityFixture(t, s, post.ID)
	parents := &accountAuthorityHeaders{Repository: repository}
	s.repository = parents
	// Many small model deltas must not copy the complete article on each frame.
	setAIResponse(s, 200, "text/event-stream", strings.Repeat(aiFrame("x", ""), 200)+aiFrame("end", "stop")+"data: [DONE]\n\n", nil)
	w, aborted := aiCommentAuthorityRequest(s, c, cookie, actor, t.Context(), nil)
	if aborted || w.Code != 200 || parents.fullReads != 1 || parents.reads < 3 || parents.reads > 10 {
		t.Fatal("draft repeated full context reads or checked every delta", aborted, w.Code, parents.fullReads, parents.reads)
	}
}

type aiCommentAuthorityFrames struct {
	frames    []string
	afterRead func()
	reads     int
}

func (r *aiCommentAuthorityFrames) Read(p []byte) (int, error) {
	r.reads++
	if len(r.frames) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.frames[0])
	r.frames[0] = r.frames[0][n:]
	if r.frames[0] == "" {
		r.frames = r.frames[1:]
	}
	runAutosaveBoundary(&r.afterRead)
	return n, nil
}

func TestAICommentAuthorityStopsRevokedStream(t *testing.T) {
	for _, kind := range []string{"text", "thinking", "usage"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, _, manager, repository, cookie, actor := newPersistentAdminTestServer(t)
			enableWritingFixture(t, s)
			post := createOwnedTestPost(t, repository, actor.UserID, actor.Label(), "ai-stream-stop", "PRIVATE-AI-PARENT")
			c := commentAuthorityFixture(t, s, post.ID)
			frame := aiFrame("Partial draft", "")
			if kind == "thinking" {
				frame = "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"Thinking\"}}]}\n\n"
			} else if kind == "usage" {
				frame = "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6}}\n\n"
			}
			reader := &aiCommentAuthorityFrames{frames: []string{frame, aiFrame(aiCommentAuthorityDraft, "stop") + "data: [DONE]\n\n"}, afterRead: func() {
				if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
					t.Fatal(err)
				}
			}}
			body := &aiCommentAuthorityResponseBody{ReadCloser: io.NopCloser(reader)}
			s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
			})}}
			w, aborted := aiCommentAuthorityRequest(s, c, cookie, actor, t.Context(), nil)
			if aborted || w.Code != 401 || reader.reads != 1 || !body.closed || strings.Contains(w.Body.String(), "draft") {
				t.Fatal("revoked stream continued", aborted, w.Code, reader.reads, body.closed)
			}
			if !s.aiState.acquire(actor.UserID) {
				t.Fatal("revoked stream leaked generation slot")
			}
			s.aiState.release(actor.UserID)
		})
	}
}
