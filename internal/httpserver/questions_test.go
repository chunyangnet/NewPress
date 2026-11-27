package httpserver

import (
	"net/http"
	"net/http/httptest"
	"newpress.local/newpress/internal/questions"
	"strings"
	"testing"
	"time"
)

func TestQuestionsRejectMalformedFilters(t *testing.T) {
	s, handler, _, _, _, _ := newPersistentAdminTestServer(t)
	store, err := questions.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetQuestionsStore(store)
	for _, query := range []string{"tag=%FF", "tag=%00", "q=a%0Ab", "status=unknown", "scope=other", "page=-1", "q=a&q=b"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/questions/?"+query, nil))
		if w.Code != 400 {
			t.Errorf("%s returned %d", query, w.Code)
		}
	}
}

func TestQuestionsPublicIsolation(t *testing.T) {
	s, handler, _, _, _, owner := newPersistentAdminTestServer(t)
	store, err := questions.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetQuestionsStore(store)
	_, err = store.Apply(t.Context(), 0, questions.Actor{ID: owner.UserID}, questions.Command{Action: "create", Key: "http-question-create", Title: "Independent public question", Body: "A **global** question", Tags: []string{"testing"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.config.CommunityEnabled = false
	for _, path := range []string{"/questions/", "/questions/1/", "/questions/?status=open", "/questions/new/"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), "data-questions") {
			t.Errorf("%s missing questions template", path)
		}
	}
	_, err = store.Apply(t.Context(), 1, questions.Actor{ID: owner.UserID, Admin: true}, questions.Command{Action: "hide", Key: "http-question-hide", Version: 1, Enabled: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/questions/1/", nil))
	if w.Code != 404 {
		t.Fatalf("hidden detail: %d", w.Code)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/questions/", nil))
	if strings.Contains(w.Body.String(), "Independent public question") {
		t.Fatal("hidden question leaked")
	}
}

func TestQuestionsUnansweredFilterUsesVisibleAnswers(t *testing.T) {
	s, handler, _, _, _, owner := newPersistentAdminTestServer(t)
	store, err := questions.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetQuestionsStore(store)
	actor := questions.Actor{ID: owner.UserID, Admin: true}
	result, err := store.Apply(t.Context(), 0, actor, questions.Command{Action: "create", Key: "unanswered-create-question", Title: "Unique unanswered marker", Body: "Question body"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/questions/?status=unanswered", nil))
		if w.Code != 200 || strings.Contains(w.Body.String(), "Unique unanswered marker") != want {
			t.Fatalf("unanswered filter: %d %s", w.Code, w.Body.String())
		}
	}
	check(true)
	result, err = store.Apply(t.Context(), 1, actor, questions.Command{Action: "answer", Key: "unanswered-create-answer", Version: result.Version, Body: "Visible answer"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	check(false)
	_, err = store.Apply(t.Context(), 1, actor, questions.Command{Action: "hide_answer", Key: "unanswered-hide-answer", Version: result.Version, AnswerID: 1, Enabled: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	check(true)
}
