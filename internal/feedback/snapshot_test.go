package feedback

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
)

func feedbackSnapshotFixture(n int) Board {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	b := Board{Slug: "ideas", Version: 1, Settings: Defaults(), NextItem: int64(n + 1), NextReply: int64(n*5 + 1), CreatedAt: now, UpdatedAt: now}
	for i := 1; i <= n; i++ {
		item := Item{ID: int64(i), AuthorID: 2, Title: "Feedback suggestion", Body: strings.Repeat("feedback details ", 20), Kind: "suggestion", Status: "new", Priority: "normal", Visibility: "public", Votes: []int64{2, 3, 4}, History: []Event{{ActorID: 2, Status: "new", Note: "Initial submission", At: now}}, CreatedAt: now, UpdatedAt: now}
		for j := 1; j <= 5; j++ {
			item.Replies = append(item.Replies, Reply{ID: int64((i-1)*5 + j), AuthorID: 3, Body: strings.Repeat("reply details ", 20), CreatedAt: now})
		}
		b.Items = append(b.Items, item)
	}
	b.Receipts = []Receipt{{ActorID: 1, Key: "create-ideas-board", Digest: strings.Repeat("a", 64), Result: Result{Slug: b.Slug, Version: 1}}}
	return b
}

func checkedFeedbackSnapshot(t testing.TB, b Board) []byte {
	t.Helper()
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeBoard(data); err != nil {
		t.Fatal("invalid board fixture:", err)
	}
	return data
}

func TestFeedbackSnapshotIsolatedFromStoredBoard(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint("disk=", disk), func(t *testing.T) {
			want := feedbackSnapshotFixture(2)
			data := checkedFeedbackSnapshot(t, want)
			path := ""
			if disk {
				path = filepath.Join(t.TempDir(), "feedback.json")
				document, _ := json.Marshal(fileState{Schema: 1, Boards: []Board{want}})
				if err := os.WriteFile(path, document, 0600); err != nil {
					t.Fatal(err)
				}
			}
			store, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if !disk {
				store.boards[want.Slug] = want
			}
			snapshot, err := store.Get(t.Context(), want.Slug)
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Settings.Kinds[0] = "changed"
			snapshot.Settings.Categories[0] = "changed"
			snapshot.Settings.Permissions[0].Read = false
			snapshot.Items[0].Private = true
			snapshot.Items[0].Votes[0] = 999
			snapshot.Items[0].Replies[0].Hidden = true
			snapshot.Items[0].History[0].Note = "changed"
			snapshot.Receipts[0].Result.Version = 999
			headers, err := store.List(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(headers) != 1 || len(headers[0].Items) != 0 || len(headers[0].Receipts) != 0 {
				t.Fatal("list exposed item or receipt details")
			}
			headers[0].Settings.Permissions[0].Moderate = true
			headers[0].Settings.Kinds[0] = "changed"
			headers[0].Settings.Categories[0] = "changed"
			got, err := store.Get(t.Context(), want.Slug)
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
				if !reflect.DeepEqual(reopened.boards[want.Slug], got) {
					t.Fatal("memory and persisted board diverged")
				}
			}
		})
	}
}

func TestFeedbackRejectedVoteDoesNotChangeStoredVotes(t *testing.T) {
	b := feedbackSnapshotFixture(1)
	before := checkedFeedbackSnapshot(t, b)
	enabled := false
	// Validation fails after removing an existing vote because the supplied time
	// predates the item. Failure must not modify the old votes' backing array.
	_, _, err := Mutate(&b, b.Slug, Actor{ID: 2, Roles: []string{"subscriber"}}, Command{Action: "vote", Key: "rejected-vote-key", Version: 1, ItemID: 1, Enabled: &enabled}, b.CreatedAt.Add(-time.Hour))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want invalid timestamp, got %v", err)
	}
	encoded, _ := json.Marshal(b)
	if string(encoded) != string(before) {
		t.Fatal("rejected vote changed the stored board")
	}
}

func BenchmarkFeedbackSnapshotRead(b *testing.B) {
	board := feedbackSnapshotFixture(500)
	checkedFeedbackSnapshot(b, board)
	store := &FileStore{boards: map[string]Board{board.Slug: board}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := store.Get(b.Context(), board.Slug); err != nil {
			b.Fatal(err)
		}
	}
}
