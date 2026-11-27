package wechat

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

func TestSnippetReferencesBlockWeChatBeforeRemotePreparation(t *testing.T) {
	for _, visibility := range []string{content.SnippetShared, content.SnippetPrivate, content.SnippetWithdrawn} {
		t.Run(visibility, func(t *testing.T) {
			s, post := fixture(t)
			access := content.SnippetAccess{Identity: "legacy:test", Actor: "test", Read: true, Manage: true}
			snippet, err := s.Content.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "source", ContentSource: "<p>retained-secret</p>", Visibility: content.SnippetShared}, access)
			if err != nil {
				t.Fatal(err)
			}
			if visibility == content.SnippetPrivate {
				_, err = s.Content.SaveSnippet(t.Context(), snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, content.SnippetInput{Title: "source", ContentSource: "<p>retained-secret</p>", Visibility: content.SnippetPrivate}, access)
			} else if visibility == content.SnippetWithdrawn {
				_, err = s.Content.WithdrawSnippet(t.Context(), snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, access)
			}
			if err != nil {
				t.Fatal(err)
			}
			post, err = s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Slug: post.Slug, Title: post.Title, ContentSource: "<p>ordinary</p>" + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: 1})})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			s.Client.HTTP = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not send") })}
			state, err := s.Store.View(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			check, err := s.CheckSync(t.Context(), post.ID, post.Version, state.Config.Version)
			if err != nil || check.Ready || len(check.Issues) == 0 || !strings.Contains(check.Issues[0].Message, "共享片段") {
				t.Fatal(check, err)
			}
			if _, err = s.Queue(t.Context(), post.ID, post.Version, "test"); err != nil {
				t.Fatal(err)
			}
			if err = s.Tick(t.Context(), time.Now()); err != nil {
				t.Fatal(err)
			}
			job := stateJob(t, s)
			if job.Status != "failed" || !strings.Contains(job.Error, "共享片段") || calls != 0 || len(job.Images) != 0 {
				t.Fatal(job, calls)
			}
			current, err := s.Content.GetByID(t.Context(), post.ID)
			if err != nil || current.Version != post.Version || current.ContentSource != post.ContentSource {
				t.Fatal("export check mutated source", err)
			}
		})
	}
}

func TestMalformedSnippetReferencesAreNotExportedAsFallback(t *testing.T) {
	for _, source := range []string{`<aside data-np-snippet="bad">fallback-secret</aside>`, `<aside data-np-snippet-version="1">unclosed-secret`} {
		plan, err := buildSyncPlan(Config{}, content.Post{Title: "test", ContentSource: source, ContentHTML: source}, "https://example.test")
		if err != nil || len(plan.issues) == 0 || !strings.Contains(plan.issues[0].Message, "共享片段") {
			t.Fatal(plan, err)
		}
	}
}
