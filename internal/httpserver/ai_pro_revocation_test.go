package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func waitForTerminalProJob(t *testing.T, s *Server) aijobs.Job {
	t.Helper()
	timeout := time.NewTimer(8 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := s.aiJobs.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Jobs) == 1 && state.Jobs[0].Terminal() {
			return state.Jobs[0]
		}
		select {
		case <-timeout.C:
			t.Fatal("workflow did not reach a terminal state")
		case <-tick.C:
		}
	}
}

func TestProWorkflowRechecksAuthorityAfterGeneration(t *testing.T) {
	for _, stage := range []string{"text", "image", "cancelled", "allowed"} {
		t.Run(stage, func(t *testing.T) {
			s, _, manager, repo, _, owner := newPersistentAdminTestServer(t)
			enableWritingFixture(t, s)
			user, _, actor := createAndLoginTestUser(t, manager, owner, "workflow-owner", "workflow-owner@example.test", "Workflow owner", "administrator")
			settings := s.aiSettings.Snapshot()
			settings.DefaultImageProvider = "images"
			settings.Providers = append(settings.Providers, aieditor.Provider{ID: "images", Name: "Fixture images", Protocol: aieditor.ProtocolOpenAIImages, BaseURL: "https://images.example.test", APIKey: "fixture-image-key", Models: []string{"gpt-image-1"}})
			if _, err := s.aiSettings.Save(settings); err != nil {
				t.Fatal(err)
			}
			revoke := func(role string) {
				_, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: user.Status, RoleSlugs: []string{role}})
				if err != nil {
					t.Error("revoke workflow authority:", err)
				}
			}
			var textCalls, imageCalls atomic.Int32
			setAIResponse(s, 200, "text/event-stream", aiFrame(`<newpress-meta>{"title":"Reviewed workflow","slug":"reviewed-workflow"}</newpress-meta>`+"\n\nArticle generated for the permission regression.", "stop")+"data: [DONE]\n\n", func(*http.Request) {
				textCalls.Add(1)
				if stage == "text" {
					// Editors retain content write access, but lose plugins:manage.
					revoke("editor")
				}
			})
			picture := image.NewNRGBA(image.Rect(0, 0, 32, 24))
			picture.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
			var imageData bytes.Buffer
			if err := png.Encode(&imageData, picture); err != nil {
				t.Fatal(err)
			}
			encoded := base64.StdEncoding.EncodeToString(imageData.Bytes())
			s.aiImages = &aieditor.ImageClient{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
				imageCalls.Add(1)
				if stage == "image" {
					revoke("author")
				}
				if stage == "cancelled" {
					state, err := s.aiJobs.Snapshot()
					if err != nil || len(state.Jobs) != 1 {
						t.Error("read running job before cancellation:", err)
					} else if err := s.aiJobs.Action(state.Epoch, state.Jobs[0].ID, state.Jobs[0].Version, "cancel", time.Now()); err != nil {
						t.Error("cancel image generation:", err)
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"` + encoded + `"}]}`))}, nil
			})}}
			state, err := s.aiJobs.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			job := aijobs.Job{Author: actor.Username, UserID: actor.UserID, Topic: "Reviewed workflow", Provider: "writer", Model: "writer-model", Language: "zh-CN", PostType: "post", Work: aijobs.WorkSpec{Kind: "workflow", Key: "revocation-workflow", PlannedTitle: "Reviewed workflow", Destination: "publish"}}
			if stage != "text" {
				job.Work.Strategy.Images = 1
			}
			mediaBefore := s.media.Snapshot().Count
			if err := s.aiJobs.AddWork(state.Epoch, []aijobs.Job{job}, time.Now()); err != nil {
				t.Fatal(err)
			}
			workerContext, cancel := context.WithCancel(t.Context())
			workerDone := make(chan struct{})
			go func() { defer close(workerDone); s.RunAIWritingQueue(workerContext) }()
			t.Cleanup(func() { cancel(); <-workerDone })
			done := waitForTerminalProJob(t, s)
			cancel()
			<-workerDone
			posts, err := repo.ListAdmin(t.Context(), content.AdminQuery{Limit: 20})
			if err != nil {
				t.Fatal(err)
			}
			if textCalls.Load() != 1 || stage != "text" && imageCalls.Load() != 1 {
				t.Fatal("fixture did not complete the expected provider calls")
			}
			if stage == "allowed" {
				if done.Status != "succeeded" || posts.Total != 1 || s.media.Snapshot().Count != mediaBefore+1 {
					t.Fatalf("authorized workflow failed: status=%s posts=%d media=%d", done.Status, posts.Total, s.media.Snapshot().Count)
				}
				return
			}
			if stage == "cancelled" {
				if done.Status != "cancelled" || posts.Total != 0 || s.media.Snapshot().Count != mediaBefore {
					t.Fatalf("cancelled workflow wrote content: status=%s posts=%d media_delta=%d", done.Status, posts.Total, s.media.Snapshot().Count-mediaBefore)
				}
				return
			}
			if done.Status != "failed" || done.Problem != aijobs.ErrActor.Error() || posts.Total != 0 || s.media.Snapshot().Count != mediaBefore {
				t.Fatalf("revoked workflow persisted side effects: status=%s posts=%d media_delta=%d", done.Status, posts.Total, s.media.Snapshot().Count-mediaBefore)
			}
		})
	}
}

func TestProModelFallbackRechecksActor(t *testing.T) {
	for _, kind := range []string{"", "metadata", "paragraph"} {
		t.Run("kind="+kind, func(t *testing.T) {
			s, _, manager, _, _, owner := newPersistentAdminTestServer(t)
			enableWritingFixture(t, s)
			user, _, actor := createAndLoginTestUser(t, manager, owner, "fallback-owner", "fallback-owner@example.test", "Fallback owner", "administrator")
			settings := s.aiSettings.Snapshot()
			settings.Providers = append(settings.Providers, aieditor.Provider{ID: "fallback", Name: "Fallback fixture", BaseURL: "https://fallback.example.test/v1", APIKey: "fixture-only-key", Models: []string{"fallback-model"}, DefaultModel: "fallback-model"})
			if _, err := s.aiSettings.Save(settings); err != nil {
				t.Fatal(err)
			}
			calls := 0
			s.aiClient = &aieditor.Client{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					role := "editor"
					if kind != "" {
						role = "subscriber"
					}
					_, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: user.Status, RoleSlugs: []string{role}})
					if err != nil {
						t.Fatal(err)
					}
				}
				return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"temporary fixture failure"}}`))}, nil
			})}}
			job := aijobs.Job{UserID: actor.UserID, Author: actor.Username, Provider: "writer", Model: "writer-model", Work: aijobs.WorkSpec{Kind: kind, Rules: aijobs.Rules{Fallback: true}}}
			_, err := s.autoText(t.Context(), job, []aieditor.Message{{Role: "user", Content: "Fixture request"}}, nil, 100)
			if !errors.Is(err, aijobs.ErrActor) || calls != 1 {
				t.Fatalf("revoked actor reached another provider: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestProEditorToolsKeepContentPermissions(t *testing.T) {
	for _, role := range []string{"bootstrap", "editor", "author", "subscriber"} {
		t.Run(role, func(t *testing.T) {
			var s *Server
			var handler http.Handler
			var cookie *http.Cookie
			var actor auth.Principal
			if role == "bootstrap" {
				s, handler, _, cookie, actor = newCommentHTTPTestServer(t)
			} else {
				var manager *auth.Manager
				var owner auth.Principal
				s, handler, manager, _, _, owner = newPersistentAdminTestServer(t)
				_, cookie, actor = createAndLoginTestUser(t, manager, owner, "tool-user", "tool-user@example.test", "Tool user", role)
			}
			enableWritingFixture(t, s)
			for _, input := range []struct{ route, body, output string }{
				{"metadata", `{"title":"Article","body":"Article details","language":"zh-CN","model":"writer:writer-model","fields":["excerpt"]}`, `{"excerpt":"Fixture editor output"}`},
				{"paragraph", `{"text":"Article paragraph","language":"zh-CN","mode":"polish","model":"writer:writer-model"}`, "Fixture editor output"},
			} {
				calls := 0
				setAIResponse(s, 200, "text/event-stream", aiFrame(input.output, "stop")+"data: [DONE]\n\n", func(*http.Request) { calls++ })
				response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/plugins/ai-pro-max/"+input.route, input.body, map[string]string{"Content-Type": "application/json", "X-CSRF-Token": actor.CSRFToken, "Origin": "https://example.test"})
				if role == "subscriber" {
					if response.Code != http.StatusForbidden || calls != 0 {
						t.Fatalf("unauthorized %s request: status=%d calls=%d", input.route, response.Code, calls)
					}
				} else if response.Code != http.StatusOK || calls != 1 || !strings.Contains(response.Body.String(), "Fixture editor output") {
					t.Fatalf("authorized %s request: status=%d calls=%d body=%s", input.route, response.Code, calls, response.Body.String())
				}
			}
		})
	}
}
