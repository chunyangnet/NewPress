package httpserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

const atomicContentAPITarget = "https://example.test/api/v1/admin/contents"

func TestAdminContentAPIAtomicFileRestartReturnsCurrentRepresentation(t *testing.T) {
	s, handler, _, cookie, principal := newAuthenticatedContentServer(t)
	path := filepath.Join(t.TempDir(), "content.json")
	repository, err := content.NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	s.repository = repository
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = "atomic-file-restart"
	payload := `{"title":"Original article","slug":"original-api-slug","content_source":"Original body"}`
	first := serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload, headers)
	if first.Code != http.StatusCreated {
		t.Fatal(first.Code, first.Body.String())
	}
	var created adminContentAPIResponse
	decodeAPIResponse(t, first, &created)
	post, err := repository.GetByID(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := inputFromPost(post)
	input.Title, input.ContentSource = "Edited after creation", "Current body"
	updated, err := repository.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: principal.Username}, input)
	if err != nil {
		t.Fatal(err)
	}
	// Reopen all persistent content and discard the process-local registry.
	reopened, err := content.NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	s.repository = reopened
	replay := serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload, headers)
	assertAtomicContentReplay(t, replay, updated)
	conflict := serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload+" ", headers)
	assertAPIError(t, conflict, http.StatusConflict, "request.idempotency_conflict")
	list, err := reopened.ListAdmin(t.Context(), content.AdminQuery{Limit: 10})
	if err != nil || list.Total != 1 {
		t.Fatal(list.Total, err)
	}
	revisions, err := reopened.ListRevisions(t.Context(), post.ID, 20)
	if err != nil || len(revisions) != 2 {
		t.Fatal(len(revisions), err)
	}
}

type uncertainContentAPIRepository struct{ content.Repository }

func (r uncertainContentAPIRepository) Submit(ctx context.Context, request content.Submission) (content.SubmissionResult, error) {
	if _, err := r.Repository.Submit(ctx, request); err != nil {
		return content.SubmissionResult{}, err
	}
	return content.SubmissionResult{}, errors.New("connection lost after durable commit")
}

func TestAdminContentAPIAtomicUncertainCommitCanBeConfirmed(t *testing.T) {
	s, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	s.repository = uncertainContentAPIRepository{repository}
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = "uncertain-content-commit"
	payload := `{"title":"Keep the original operation","content_source":"Original request"}`
	first := serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload, headers)
	assertAPIError(t, first, http.StatusServiceUnavailable, "request.idempotency_result_unconfirmed")
	list, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 10})
	if err != nil || list.Total != 1 {
		t.Fatal(list.Total, err)
	}
	post, err := repository.GetByID(t.Context(), list.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertAtomicContentReplay(t, serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload, headers), post)
	revisions, err := repository.ListRevisions(t.Context(), post.ID, 20)
	if err != nil || len(revisions) != 1 {
		t.Fatal(len(revisions), err)
	}
}

type unavailableContentAPIReceiptRepository struct{ content.Repository }

func (r unavailableContentAPIReceiptRepository) FindSubmission(context.Context, string, string) (content.SubmissionResult, error) {
	return content.SubmissionResult{}, errors.New("receipt read unavailable")
}

func TestAdminContentAPIAtomicLookupFailureDoesNotCreate(t *testing.T) {
	s, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	s.repository = unavailableContentAPIReceiptRepository{repository}
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = "unavailable-receipt-read"
	response := serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, `{"title":"Must wait for a confirmed lookup"}`, headers)
	assertAPIError(t, response, http.StatusServiceUnavailable, "request.idempotency_result_unconfirmed")
	list, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 10})
	if err != nil || list.Total != 0 {
		t.Fatal(list.Total, err)
	}
}

func TestAdminContentAPIAtomicReplayBeforeScheduleValidation(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	key := "already-published-schedule"
	payload := `{"title":"Scheduled article","status":"future","published_at":"2020-01-01T12:00:00Z"}`
	namespace := principal.Username + "\x00/api/v1/admin/contents\x00" + key
	fingerprint := sha256.Sum256([]byte(payload))
	input := content.PostInput{Title: "Scheduled article now published", Status: content.StatusPublish}
	var post content.Post
	result, err := repository.Submit(t.Context(), content.Submission{
		Key: fmt.Sprintf("%x", sha256.Sum256([]byte(namespace))), Fingerprint: fmt.Sprintf("%x", fingerprint), Action: "create", Input: input,
	})
	if err != nil {
		t.Fatal(err)
	}
	post, err = repository.GetByID(t.Context(), result.Receipt.PostID)
	if err != nil {
		t.Fatal(err)
	}
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = key
	assertAtomicContentReplay(t, serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload, headers), post)
}

func TestAdminContentAPIAtomicConcurrentIndependentRegistries(t *testing.T) {
	_, handlerA, repository, cookieA, principalA := newAuthenticatedContentServer(t)
	second, handlerB, _, cookieB, principalB := newAuthenticatedContentServer(t)
	second.repository = repository
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 8)
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			handler, cookie, principal := handlerA, cookieA, principalA
			if i%2 != 0 {
				handler, cookie, principal = handlerB, cookieB, principalB
			}
			headers := authenticatedAPIHeaders(principal)
			headers["Idempotency-Key"] = "concurrent-api-creation"
			<-start
			responses <- serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, `{"title":"One article","slug":"concurrent-api-creation"}`, headers)
		}()
	}
	close(start)
	workers.Wait()
	close(responses)
	createdCount := 0
	var id int64
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("concurrent creation: %d %s", response.Code, response.Body.String())
		}
		var body adminContentAPIResponse
		decodeAPIResponse(t, response, &body)
		if body.ID <= 0 || (id != 0 && id != body.ID) {
			t.Fatalf("inconsistent content identity: %d vs %d", id, body.ID)
		}
		id = body.ID
		if response.Header().Get("Idempotency-Replayed") != "true" {
			createdCount++
		}
	}
	list, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 10})
	if err != nil || list.Total != 1 || createdCount != 1 {
		t.Fatal(list.Total, createdCount, err)
	}
}

func TestAdminContentAPIValidationDoesNotCreateReceipt(t *testing.T) {
	_, handler, _, cookie, principal := newAuthenticatedContentServer(t)
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = "validation-content"
	payload := `{"title":"Valid request"}`
	headers["Idempotency-Key"] = "invalid-first-content"
	invalid := serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, `{"title":""}`, headers)
	assertAPIError(t, invalid, http.StatusUnprocessableEntity, "content.validation_failed")
	valid := serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload, headers)
	if valid.Code != http.StatusCreated || valid.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal(valid.Code, valid.Body.String())
	}
}

func TestAdminContentAPIAtomicReplayRechecksIdentityObjectAndRequestBoundaries(t *testing.T) {
	s, handler, manager, repository, _, owner := newPersistentAdminTestServer(t)
	author, cookie, principal := createAndLoginTestUser(t, manager, owner, "api-author", "api-author@example.test", "Author", "author")
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = "author-api-submission"
	payload := `{"title":"Author article","slug":"author-atomic-article"}`
	first := serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload, headers)
	if first.Code != http.StatusCreated {
		t.Fatal(first.Code, first.Body.String())
	}
	var created adminContentAPIResponse
	decodeAPIResponse(t, first, &created)
	post, err := repository.GetByID(t.Context(), created.ID)
	if err != nil || post.AuthorID != author.ID {
		t.Fatal(post, err)
	}
	token, renewed, err := manager.AuthenticateContext(t.Context(), "api-renewed-session", author.Username, "Account password for "+author.Username+" 2026")
	if err != nil {
		t.Fatal(err)
	}
	cookie = &http.Cookie{Name: auth.CookieName, Value: token, Path: "/"}
	headers = authenticatedAPIHeaders(renewed)
	headers["Idempotency-Key"] = "author-api-submission"
	assertAtomicContentReplay(t, serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload, headers), post)
	for _, boundary := range []struct {
		name, value, code string
	}{
		{"X-CSRF-Token", "expired-csrf", "auth.csrf_validation_failed"},
		{"Origin", "https://attacker.test", "auth.cross_site_request_rejected"},
	} {
		original := headers[boundary.name]
		headers[boundary.name] = boundary.value
		assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload, headers), http.StatusForbidden, boundary.code)
		headers[boundary.name] = original
	}
	assertAPIError(t, serveContentAPI(handler, nil, http.MethodPost, atomicContentAPITarget, payload, headers), http.StatusUnauthorized, "auth.authentication_required")
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, `{"title":"one","title":"two"}`, headers), http.StatusBadRequest, "request.invalid_json")
	settings := s.siteSettings.Snapshot()
	settings.Settings.API.AdminContentEnabled = false
	if _, _, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload, headers), http.StatusNotFound, "request.not_found")
	settings = s.siteSettings.Snapshot()
	settings.Settings.API.AdminContentEnabled = true
	if _, _, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil {
		t.Fatal(err)
	}
	// A second authenticated identity may use the same key for its own creation.
	other, otherCookie, otherPrincipal := createAndLoginTestUser(t, manager, owner, "api-other", "api-other@example.test", "Other", "author")
	otherHeaders := authenticatedAPIHeaders(otherPrincipal)
	otherHeaders["Idempotency-Key"] = headers["Idempotency-Key"]
	otherResponse := serveContentAPI(handler, otherCookie, http.MethodPost, atomicContentAPITarget, `{"title":"Other article","slug":"other-atomic-article"}`, otherHeaders)
	if otherResponse.Code != http.StatusCreated || otherResponse.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal(otherResponse.Code, otherResponse.Body.String())
	}
	// A stored receipt is not an authorization grant. Exercise both receipt
	// stores with a result owned by somebody else.
	headers["Idempotency-Key"] = "foreign-content-receipt"
	namespace := principal.Username + "\x00/api/v1/admin/contents\x00" + headers["Idempotency-Key"]
	_, err = repository.Submit(t.Context(), content.Submission{
		Key: fmt.Sprintf("%x", sha256.Sum256([]byte(namespace))), Fingerprint: fmt.Sprintf("%x", sha256.Sum256([]byte(payload))),
		Action: "create", Input: content.PostInput{Title: "Private foreign article", Slug: "foreign-content-receipt", AuthorID: other.ID, AuthorName: other.DisplayName},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, payload, headers), http.StatusForbidden, "content.owner_required")

}

func TestAdminContentAPIAtomicMediaValidationRemainsEnabled(t *testing.T) {
	_, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = "missing-media-submission"
	invalid := serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, `{"title":"Media reference","cover_url":"/media/999999/does-not-exist.png"}`, headers)
	assertAPIError(t, invalid, http.StatusUnprocessableEntity, "content.validation_failed")
	list, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 10})
	if err != nil || list.Total != 0 {
		t.Fatal(list.Total, err)
	}
	valid := serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, `{"title":"Media reference"}`, headers)
	if valid.Code != http.StatusCreated {
		t.Fatal(valid.Code, valid.Body.String())
	}
}

func TestAdminContentAPIAtomicNoKeyKeepsExistingCreatePath(t *testing.T) {
	s, handler, repository, cookie, principal := newAuthenticatedContentServer(t)
	s.repository = unavailableContentAPIReceiptRepository{repository}
	response := serveContentAPI(handler, cookie, http.MethodPost, atomicContentAPITarget, `{"title":"Without an idempotency key"}`, authenticatedAPIHeaders(principal))
	if response.Code != http.StatusCreated || response.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal(response.Code, response.Body.String())
	}
}

func assertAtomicContentReplay(t *testing.T, response *httptest.ResponseRecorder, post content.Post) {
	t.Helper()
	if response.Code != http.StatusCreated || response.Header().Get("Idempotency-Replayed") != "true" ||
		response.Header().Get("Location") != fmt.Sprintf("/api/v1/admin/contents/%d", post.ID) || response.Header().Get("ETag") != contentAPIETag(post) {
		t.Fatalf("replay: %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var body adminContentAPIResponse
	decodeAPIResponse(t, response, &body)
	if body.ID != post.ID || body.Version != post.Version || body.Title != post.Title || body.ContentSource != post.ContentSource {
		t.Fatalf("replayed stale/different representation: %#v", body)
	}
}
