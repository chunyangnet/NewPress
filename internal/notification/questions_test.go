package notification

import (
	"fmt"
	"newpress.local/newpress/internal/questions"
	"strings"
	"testing"
	"time"
)

func TestQuestionNotificationPaginationAndVisibility(t *testing.T) {
	s, _, _, _ := notificationFixture(t)
	store, err := questions.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.Questions = store
	now := time.Now()
	_, err = store.Apply(t.Context(), 0, questions.Actor{ID: 1}, questions.Command{Action: "create", Key: "notify-create", Title: "Global question", Body: "Details"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 21; i++ {
		_, err = store.Apply(t.Context(), 1, questions.Actor{ID: 2}, questions.Command{Action: "answer", Key: fmt.Sprintf("notify-answer-%d", i), Version: int64(i), Body: "Answer"}, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	state, _ := store.Snapshot(t.Context())
	event := state.Events[len(state.Events)-1]
	delivery := Delivery{UserID: 1, Event: event, Kind: Reply}
	view, ok, err := s.Resolve(t.Context(), delivery, now)
	if err != nil || !ok || !strings.Contains(view.URL, "?page=2#answer-21") {
		t.Fatal(view, ok, err)
	}
	s.QuestionsEnabled = func() bool { return false }
	if _, ok, err = s.Resolve(t.Context(), delivery, now); err != nil || ok {
		t.Fatal("disabled question notification", ok, err)
	}
	s.QuestionsEnabled = nil
	_, err = store.Apply(t.Context(), 1, questions.Actor{ID: 3, Admin: true}, questions.Command{Action: "hide_answer", Key: "notify-hide-answer", Version: 22, AnswerID: 21, Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err = s.Resolve(t.Context(), delivery, now); err != nil || ok {
		t.Fatal("hidden answer notification", ok, err)
	}
}
