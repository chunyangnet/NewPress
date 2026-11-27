package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
)

type lostSnippetDraftAcknowledgement struct {
	content.Repository
	fail bool
}

func (r *lostSnippetDraftAcknowledgement) Submit(ctx context.Context, u content.Submission) (content.SubmissionResult, error) {
	v, err := r.Repository.Submit(ctx, u)
	if err == nil && r.fail {
		r.fail = false
		return content.SubmissionResult{}, errors.New("injected lost commit acknowledgement")
	}
	return v, err
}

func TestSnippetDraftAPILostAcknowledgementReplays(t *testing.T) {
	s, handler, r, cookie, principal := newAuthenticatedContentServer(t)
	a := snippetAccess(principal)
	snippet, err := r.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>copied</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(t.Context(), content.PostInput{Title: "source", ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	u := content.SnippetDraftRequest{PostID: p.ID, Version: 1}
	plan, err := r.PreviewSnippetDraft(t.Context(), u, a)
	if err != nil {
		t.Fatal(err)
	}
	u.Digest, u.RequestKey = plan.Digest, strings.Repeat("f", 64)
	body, _ := json.Marshal(u)
	s.repository = &lostSnippetDraftAcknowledgement{Repository: s.repository, fail: true}
	url := fmt.Sprintf("https://example.test/api/v1/admin/contents/%d/snippet-draft", p.ID)
	w := serveContentAPI(handler, cookie, "POST", url, string(body), authenticatedAPIHeaders(principal))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "result_unconfirmed") {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err = r.WithdrawSnippet(t.Context(), snippet.ID, content.Mutation{ExpectedVersion: 1}, a); err != nil {
		t.Fatal(err)
	}
	w = serveContentAPI(handler, cookie, "POST", url, string(body), authenticatedAPIHeaders(principal))
	if w.Code != 201 || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal(w.Code, w.Body.String())
	}
	all, _ := r.ListAdmin(t.Context(), content.AdminQuery{Limit: 20})
	if all.Total != 2 {
		t.Fatal("duplicate", all.Total)
	}
}

func TestSnippetDraftAPIConfirmationAndAuthorization(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	_, authorCookie, author := createAndLoginTestUser(t, manager, owner, "draft-author", "draft-author@example.test", "作者", "author")
	a := snippetAccess(owner)
	snippet, err := s.repository.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "shared", ContentSource: "<p>pinned</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.repository.Create(t.Context(), content.PostInput{Title: "author source", AuthorID: author.UserID, ContentSource: contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: 1})})
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("https://example.test/api/v1/admin/contents/%d/snippet-draft", p.ID)
	body := fmt.Sprintf(`{"post_id":%d,"version":1}`, p.ID)
	for _, tc := range []struct {
		method, path, body string
		cookie             *http.Cookie
		headers            map[string]string
		status             int
	}{
		{"POST", base + "/preview", body, nil, nil, 401},
		{"POST", base + "/preview", body, authorCookie, map[string]string{"Content-Type": "application/json", "Origin": "https://example.test"}, 403},
		{"GET", base, "", cookie, nil, 405},
		{"POST", base + "/preview", strings.TrimSuffix(body, "}") + `,"content_html":"injected"}`, authorCookie, authenticatedAPIHeaders(author), 400},
	} {
		w := serveContentAPI(handler, tc.cookie, tc.method, tc.path, tc.body, tc.headers)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	w := serveContentAPI(handler, authorCookie, "POST", base+"/preview", body, authenticatedAPIHeaders(author))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	var plan content.SnippetDraftPlan
	decodeAPIResponse(t, w, &plan)
	request := content.SnippetDraftRequest{PostID: p.ID, Version: 1, Digest: plan.Digest, RequestKey: strings.Repeat("a", 64)}
	raw, _ := json.Marshal(request)
	// Digest from the author's preview cannot be confirmed by another identity.
	w = serveContentAPI(handler, cookie, "POST", base, string(raw), authenticatedAPIHeaders(owner))
	if w.Code != 412 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = serveContentAPI(handler, authorCookie, "POST", base, string(raw), authenticatedAPIHeaders(author))
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		ID       int64 `json:"id"`
		Version  int64 `json:"version"`
		Replayed bool  `json:"replayed"`
	}
	decodeAPIResponse(t, w, &created)
	if created.ID == p.ID || created.Version != 1 || created.Replayed {
		t.Fatal(created)
	}
	if _, err = s.repository.WithdrawSnippet(t.Context(), snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, a); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		w = serveContentAPI(handler, authorCookie, "POST", base, string(raw), authenticatedAPIHeaders(author))
		if w.Code != 201 || w.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	request.RequestKey = strings.Repeat("b", 64)
	raw, _ = json.Marshal(request)
	w = serveContentAPI(handler, authorCookie, "POST", base, string(raw), authenticatedAPIHeaders(author))
	if w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	original, _ := s.repository.GetByID(t.Context(), p.ID)
	draft, _ := s.repository.GetByID(t.Context(), created.ID)
	if original.Version != 1 || original.ContentSource != p.ContentSource || draft.Status != content.StatusDraft || strings.Contains(draft.ContentSource, "data-np-snippet") || !strings.Contains(draft.ContentSource, "pinned") {
		t.Fatal(original, draft)
	}
	// Another author's source is hidden before even returning a preview.
	other, _ := s.repository.Create(t.Context(), content.PostInput{AuthorID: owner.UserID, Title: "private other", ContentSource: p.ContentSource})
	url := fmt.Sprintf("https://example.test/api/v1/admin/contents/%d/snippet-draft/preview", other.ID)
	w = serveContentAPI(handler, authorCookie, "POST", url, fmt.Sprintf(`{"post_id":%d,"version":1}`, other.ID), authenticatedAPIHeaders(author))
	if w.Code != 404 || strings.Contains(w.Body.String(), "private other") {
		t.Fatal(w.Code, w.Body.String())
	}
}
