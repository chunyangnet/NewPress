package httpserver

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/navigation"
)

const atomicNavigationURL = "https://example.test/api/v1/admin/navigation/menus"

func TestNavigationAtomicFileRestartAndDeletedResult(t *testing.T) {
	s, handler, _, cookie, principal := newNavigationHTTPTestServer(t)
	path := filepath.Join(t.TempDir(), "navigation.json")
	store, err := navigation.NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	s.navigation = store
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = "navigation-restart"
	payload := `{"name":"Original menu","slug":"original-menu","locations":["mobile"]}`
	first := serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, payload, headers)
	if first.Code != http.StatusCreated {
		t.Fatal(first.Code, first.Body.String())
	}
	var created adminNavigationMenuAPIResponse
	decodeAPIResponse(t, first, &created)
	updated, err := store.Update(t.Context(), created.ID, navigation.Mutation{ExpectedVersion: 1}, navigation.MenuInput{Name: "Current menu", Slug: "original-menu", Locations: []string{navigation.LocationMobile}})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := navigation.NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	s.navigation = reopened
	replay := serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, payload, headers)
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" || replay.Header().Get("ETag") != navigationMenuAPIETag(updated) {
		t.Fatal(replay.Code, replay.Body.String())
	}
	var current adminNavigationMenuAPIResponse
	decodeAPIResponse(t, replay, &current)
	if current.ID != created.ID || current.Name != updated.Name || current.Version != 2 {
		t.Fatal(current)
	}
	if err := reopened.Delete(t.Context(), current.ID, navigation.Mutation{ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	reopened, err = navigation.NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	s.navigation = reopened
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, payload, headers), http.StatusConflict, "request.idempotency_result_unavailable")
	menus, err := reopened.List(t.Context())
	if err != nil || len(menus) != 0 {
		t.Fatal(menus, err)
	}
}

type uncertainNavigationCreationStore struct{ navigation.Store }

func (s uncertainNavigationCreationStore) CreateOnce(ctx context.Context, request navigation.Creation) (navigation.CreationResult, error) {
	if _, err := s.Store.CreateOnce(ctx, request); err != nil {
		return navigation.CreationResult{}, err
	}
	return navigation.CreationResult{}, errors.New("lost acknowledgement after navigation commit")
}

type unavailableNavigationReceiptStore struct{ navigation.Store }

func (s unavailableNavigationReceiptStore) FindCreation(context.Context, string, string) (navigation.CreationResult, error) {
	return navigation.CreationResult{}, errors.New("navigation receipt read unavailable")
}

func TestNavigationAtomicUncertainCommitAndReadFailure(t *testing.T) {
	s, handler, store, cookie, principal := newNavigationHTTPTestServer(t)
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = "uncertain-navigation"
	payload := `{"name":"Uncertain","slug":"uncertain"}`
	s.navigation = unavailableNavigationReceiptStore{store}
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, payload, headers), http.StatusServiceUnavailable, "request.idempotency_result_unconfirmed")
	menus, err := store.List(t.Context())
	if err != nil || len(menus) != 0 {
		t.Fatal(menus, err)
	}
	s.navigation = uncertainNavigationCreationStore{store}
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, payload, headers), http.StatusServiceUnavailable, "request.idempotency_result_unconfirmed")
	replay := serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, payload, headers)
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal(replay.Code, replay.Body.String())
	}
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, payload+" ", headers), http.StatusConflict, "request.idempotency_conflict")
	menus, err = store.List(t.Context())
	if err != nil || len(menus) != 1 {
		t.Fatal(menus, err)
	}
}

func TestNavigationAtomicReplayAuthorizationAndNoReceiptDisclosure(t *testing.T) {
	s, handler, manager, _, cookie, principal := newPersistentAdminTestServer(t)
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = "authorized-navigation"
	payload := `{"name":"Private receipt","slug":"private-receipt"}`
	first := serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, payload, headers)
	if first.Code != http.StatusCreated {
		t.Fatal(first.Code, first.Body.String())
	}
	assertAPIError(t, serveContentAPI(handler, nil, http.MethodPost, atomicNavigationURL, payload, headers), http.StatusUnauthorized, "auth.authentication_required")
	headers["X-CSRF-Token"] = "expired"
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, payload, headers), http.StatusForbidden, "auth.csrf_validation_failed")
	headers["X-CSRF-Token"] = principal.CSRFToken
	headers["Origin"] = "https://attacker.test"
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, payload, headers), http.StatusForbidden, "auth.cross_site_request_rejected")
	headers["Origin"] = "https://example.test"
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, `{"name":"one","name":"two"}`, headers), http.StatusBadRequest, "request.invalid_json")
	_, authorCookie, author := createAndLoginTestUser(t, manager, principal, "nav-author", "nav-author@example.test", "Author", "author")
	authorHeaders := authenticatedAPIHeaders(author)
	authorHeaders["Idempotency-Key"] = headers["Idempotency-Key"]
	assertAPIError(t, serveContentAPI(handler, authorCookie, http.MethodPost, atomicNavigationURL, payload, authorHeaders), http.StatusForbidden, "auth.capability_required")
	list := serveContentAPI(handler, cookie, http.MethodGet, atomicNavigationURL, "", nil)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(principal.Username+"\x00navigation:create\x00"+headers["Idempotency-Key"])))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), hash) || strings.Contains(list.Body.String(), "fingerprint") || strings.Contains(list.Body.String(), "creations") {
		t.Fatal("receipt leaked", list.Code, list.Body.String())
	}
	// Ordinary creates do not consult either receipt store.
	s.navigation = unavailableNavigationReceiptStore{s.navigation}
	plain := serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, `{"name":"No key","slug":"no-key"}`, authenticatedAPIHeaders(principal))
	if plain.Code != http.StatusCreated {
		t.Fatal(plain.Code, plain.Body.String())
	}
}

func TestNavigationAtomicValidationDoesNotCreateReceipt(t *testing.T) {
	_, handler, _, cookie, principal := newNavigationHTTPTestServer(t)
	headers := authenticatedAPIHeaders(principal)
	headers["Idempotency-Key"] = "legacy-menu-key"
	headers["Idempotency-Key"] = "navigation-invalid-first"
	assertAPIError(t, serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, `{"name":""}`, headers), http.StatusUnprocessableEntity, "navigation.validation_failed")
	valid := serveContentAPI(handler, cookie, http.MethodPost, atomicNavigationURL, `{"name":"Corrected menu","slug":"corrected-menu"}`, headers)
	if valid.Code != http.StatusCreated || valid.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal(valid.Code, valid.Body.String())
	}
}
