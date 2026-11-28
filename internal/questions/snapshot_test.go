package questions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
)

func questionSnapshotFixture(n int) State {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s := Empty()
	for i := 1; i <= n; i++ {
		id := int64(i)
		q := Question{ID: id, Version: 8, AuthorID: 1, Title: "Snapshot question", Body: strings.Repeat("question details ", 20), Tags: []string{"storage", "permissions"}, AcceptedID: 1, CreatedAt: now, UpdatedAt: now,
			Notes: []Note{{ID: 1, AuthorID: 2, Body: "Question note", At: now}}, Reports: []Report{{ID: 1, AuthorID: 3, AnswerID: 1, Reason: "Please review this answer", At: now}}}
		for j := 1; j <= 5; j++ {
			q.Answers = append(q.Answers, Answer{ID: int64(j), AuthorID: 2, Body: strings.Repeat("answer details ", 20), CreatedAt: now, Notes: []Note{{ID: 1, AuthorID: 1, Body: "Answer note", At: now}}})
		}
		s.Items = append(s.Items, q)
		s.Receipts = append(s.Receipts, Receipt{Actor: 1, Key: fmt.Sprintf("snapshot-%08d", i), Digest: strings.Repeat("a", 64), Result: Result{ID: id, Version: 8, AnswerID: 1}})
		s.Events = append(s.Events, activity.Event{Key: fmt.Sprintf("questions:%d:8", id), Kind: "questions", ObjectID: id, UserID: 1, Version: 8, Status: "accept", ReplyID: 1, Recipients: []int64{2}, At: now, Due: now})
	}
	s.NextID = int64(n + 1)
	return s
}

func checkedQuestionSnapshot(t testing.TB, s State) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Decode(b); err != nil {
		t.Fatal("invalid snapshot fixture:", err)
	}
	return b
}

func TestQuestionSnapshotIsolatedFromStoredState(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint("disk=", disk), func(t *testing.T) {
			want := questionSnapshotFixture(2)
			data := checkedQuestionSnapshot(t, want)
			path := ""
			if disk {
				path = filepath.Join(t.TempDir(), "questions.json")
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			store, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if !disk {
				store.state = want
			}
			snapshot, err := store.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Items[0].Hidden = true
			snapshot.Items[0].Tags[0] = "changed"
			snapshot.Items[0].Answers[0].Hidden = true
			snapshot.Items[0].Answers[0].Notes[0].Body = "changed"
			snapshot.Items[0].Notes[0].Body = "changed"
			snapshot.Items[0].Reports[0].Resolved = true
			snapshot.Receipts[0].Result.Version = 999
			snapshot.Events[0].Status = "changed"
			snapshot.Events[0].Recipients[0] = 999
			got, err := store.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(got)
			if string(encoded) != string(data) {
				t.Fatal("mutating a snapshot changed stored content or authorization")
			}
			if disk {
				reopened, err := NewFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(reopened.state, got) {
					t.Fatal("memory and persisted snapshot diverged")
				}
			}
		})
	}
}

func TestQuestionFailedModerationPreservesAcceptedAnswer(t *testing.T) {
	s := questionSnapshotFixture(1)
	s.Items[0].Version = 20000
	s.Events = make([]activity.Event, 20000)
	for i := range s.Events {
		version := int64(i + 1)
		s.Events[i] = activity.Event{Key: fmt.Sprintf("questions:1:%d", version), Kind: "questions", ObjectID: 1, UserID: 1, Version: version, Status: "accept", ReplyID: 1, Recipients: []int64{2}, At: s.Items[0].CreatedAt, Due: s.Items[0].CreatedAt}
	}
	before := checkedQuestionSnapshot(t, s)
	next, _, err := Apply(s, 1, Actor{ID: 3, Admin: true}, Command{Action: "hide_answer", Key: "failed-moderation", Version: 20000, AnswerID: 1, Enabled: true}, s.Items[0].CreatedAt)
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("want capacity rejection, got %v", err)
	}
	for _, state := range []State{s, next} {
		encoded, _ := json.Marshal(state)
		if string(encoded) != string(before) {
			t.Fatal("rejected moderation changed acceptance, visibility or the outbox")
		}
	}
}

func TestQuestionDecodeRejectsInvalidReferences(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*State)
	}{
		{"duplicate_question", func(s *State) { s.Items[1].ID = s.Items[0].ID }},
		{"receipt_missing_question", func(s *State) { s.Receipts[0].Result.ID = 99 }},
		{"receipt_future_version", func(s *State) { s.Receipts[0].Result.Version = 99 }},
		{"receipt_missing_answer", func(s *State) { s.Receipts[0].Result.AnswerID = 99 }},
		{"event_missing_question", func(s *State) { s.Events[0].ObjectID = 99 }},
		{"event_future_version", func(s *State) { s.Events[0].Version = 99 }},
		{"event_missing_answer", func(s *State) { s.Events[0].ReplyID = 99 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := questionSnapshotFixture(2)
			checkedQuestionSnapshot(t, s)
			tc.edit(&s)
			data, _ := json.Marshal(s)
			if _, err := Decode(data); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want invalid reference, got %v", err)
			}
		})
	}
}

func BenchmarkQuestionSnapshotRead(b *testing.B) {
	s := questionSnapshotFixture(500)
	checkedQuestionSnapshot(b, s)
	store := &FileStore{state: s}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := store.Snapshot(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQuestionDecode(b *testing.B) {
	for _, n := range []int{500, 5000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			data := checkedQuestionSnapshot(b, questionSnapshotFixture(n))
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := Decode(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
