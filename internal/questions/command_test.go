package questions

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestMalformedCommandsNeverMutate(t *testing.T) {
	now := time.Now()
	state, _, err := Apply(Empty(), 0, Actor{ID: 1}, Command{Action: "create", Key: "initial-question", Title: "Question", Body: "Details"}, now)
	if err != nil {
		t.Fatal(err)
	}
	cases := []Command{
		{Action: "note", AnswerID: -1, Body: "Must not become a question note"},
		{Action: "answer", AnswerID: 1, Body: "Wrong target"},
		{Action: "edit_answer", Body: "Missing target"},
		{Action: "accept", AnswerID: 1, Body: "Ignored body"},
		{Action: "hide", AnswerID: 1, Enabled: true},
		{Action: "hide_answer", Enabled: true},
		{Action: "resolve_report"},
		{Action: "report", ReportID: 1, Body: "Wrong target"},
		{Action: "answer", Title: "Ignored title", Body: "Answer"},
		{Action: "answer", Enabled: true, Body: "Answer"},
		{Action: "unknown"},
	}
	for _, c := range cases {
		t.Run(c.Action+"-"+c.Body, func(t *testing.T) {
			c.Key = "invalid-command"
			c.Version = 1
			next, _, err := Apply(state, 1, Actor{ID: 1, Admin: true}, c, now)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v", err)
			}
			if !reflect.DeepEqual(next, state) {
				t.Fatal("invalid command changed state")
			}
		})
	}
}

func TestCommandShapeAcceptsSupportedActions(t *testing.T) {
	for _, c := range []Command{
		{Action: "create", Title: "Q", Body: "B"}, {Action: "edit", Title: "Q", Body: "B"},
		{Action: "answer", Body: "B"}, {Action: "edit_answer", AnswerID: 1, Body: "B"},
		{Action: "note", Body: "B"}, {Action: "note", AnswerID: 1, Body: "B"},
		{Action: "report", Body: "B"}, {Action: "accept"}, {Action: "accept", AnswerID: 1},
		{Action: "hide", Enabled: true}, {Action: "lock"}, {Action: "hide_answer", AnswerID: 1},
		{Action: "resolve_report", ReportID: 1},
	} {
		if !validCommand(c) {
			t.Errorf("rejected %+v", c)
		}
	}
}
