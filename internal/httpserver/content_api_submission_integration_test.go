package httpserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/database"
)

func TestPostgreSQLContentAPIAtomicCreation(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	otherPool, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer otherPool.Close()
	repository, other := database.NewPostRepository(pool), database.NewPostRepository(otherPool)
	first, handlerA, _, cookieA, principalA := newAuthenticatedContentServer(t)
	second, handlerB, _, cookieB, principalB := newAuthenticatedContentServer(t)
	first.repository, second.repository = repository, other
	stamp := fmt.Sprintf("api-atomic-%d", time.Now().UnixNano())
	var postIDs []int64
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		for _, id := range postIDs {
			pool.Exec(cleanup, `DELETE FROM np_content_submissions WHERE post_id=$1`, id)
			pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=$1`, id)
		}
	}()
	keyHash := func(key string) [sha256.Size]byte {
		hash := sha256.Sum256([]byte(principalA.Username + "\x00/api/v1/admin/contents\x00" + key))
		return hash
	}
	send := func(handler http.Handler, cookie *http.Cookie, csrf, key, payload string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, atomicContentAPITarget, strings.NewReader(payload)).WithContext(ctx)
		request.AddCookie(cookie)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://example.test")
		request.Header.Set("X-CSRF-Token", csrf)
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	key := stamp + "-create"
	payload := fmt.Sprintf(`{"title":%q,"slug":%q,"content_source":"Original body"}`, stamp, stamp)
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 8)
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if i%2 == 0 {
				responses <- send(handlerA, cookieA, principalA.CSRFToken, key, payload)
			} else {
				responses <- send(handlerB, cookieB, principalB.CSRFToken, key, payload)
			}
		}()
	}
	close(start)
	workers.Wait()
	close(responses)
	var id int64
	createdCount := 0
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatal(response.Code, response.Body.String())
		}
		var body adminContentAPIResponse
		decodeAPIResponse(t, response, &body)
		if id != 0 && id != body.ID {
			t.Fatal("concurrent identities differ", id, body.ID)
		}
		id = body.ID
		if response.Header().Get("Idempotency-Replayed") != "true" {
			createdCount++
		}
	}
	postIDs = append(postIDs, id)
	if createdCount != 1 || id <= 0 {
		t.Fatal(createdCount, id)
	}
	post, err := repository.GetByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	input := inputFromPost(post)
	input.Title, input.ContentSource = stamp+" edited", "Current body"
	post, err = repository.Update(ctx, id, content.Mutation{ExpectedVersion: post.Version, Actor: principalA.Username}, input)
	if err != nil {
		t.Fatal(err)
	}
	assertAtomicContentReplay(t, send(handlerB, cookieB, principalB.CSRFToken, key, payload), post)
	assertAPIError(t, send(handlerB, cookieB, principalB.CSRFToken, key, payload+" "), http.StatusConflict, "request.idempotency_conflict")
	revisions, err := other.ListRevisions(ctx, id, 20)
	if err != nil || len(revisions) != 2 {
		t.Fatal(len(revisions), err)
	}
	// A receipt insert failure happens after the content/revision writes. All
	// of those writes must roll back, leaving no article or revision behind.
	failingKey := stamp + "-receipt-failure"
	failingHash := keyHash(failingKey)
	failingSlug := stamp + "-rollback"
	failingPayload := fmt.Sprintf(`{"title":"Receipt rollback","slug":%q}`, failingSlug)
	constraint := "api_receipt_failure_" + fmt.Sprintf("%x", failingHash)[:12]
	if _, err := pool.Exec(ctx, `ALTER TABLE np_content_submissions ADD CONSTRAINT `+constraint+` CHECK (key_hash <> '`+fmt.Sprintf("%x", failingHash)+`')`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `ALTER TABLE np_content_submissions DROP CONSTRAINT IF EXISTS `+constraint)
	assertAPIError(t, send(handlerA, cookieA, principalA.CSRFToken, failingKey, failingPayload), http.StatusServiceUnavailable, "request.idempotency_result_unconfirmed")
	var posts, receipts, revisionCount int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM np_posts WHERE slug=$1),
		(SELECT count(*) FROM np_content_submissions WHERE key_hash=$2),
		(SELECT count(*) FROM np_post_revisions WHERE snapshot->>'slug'=$1)`, failingSlug, fmt.Sprintf("%x", failingHash)).Scan(&posts, &receipts, &revisionCount); err != nil || posts != 0 || receipts != 0 || revisionCount != 0 {
		t.Fatal("partial commit after receipt failure", posts, receipts, revisionCount, err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE np_content_submissions DROP CONSTRAINT `+constraint); err != nil {
		t.Fatal(err)
	}
	retried := send(handlerB, cookieB, principalB.CSRFToken, failingKey, failingPayload)
	if retried.Code != http.StatusCreated || retried.Header().Get("Idempotency-Replayed") != "" {
		t.Fatal(retried.Code, retried.Body.String())
	}
	var retriedBody adminContentAPIResponse
	decodeAPIResponse(t, retried, &retriedBody)
	postIDs = append(postIDs, retriedBody.ID)
	revisions, err = repository.ListRevisions(ctx, retriedBody.ID, 20)
	if err != nil || len(revisions) != 1 {
		t.Fatal(len(revisions), err)
	}
	t.Log("two independent pools: concurrent creation, current representation replay and atomic receipt rollback passed")
}
