package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreReadReceiptsSurviveClockRollback(t *testing.T) {
	for _, action := range []string{"conversation", "read", "read_all"} {
		t.Run(action, func(t *testing.T) {
			now := time.Now().UTC()
			future := now.Add(24 * time.Hour)
			state := document{NextID: 8, Messages: []Message{}, Notices: []Notice{}}
			for i := range 7 {
				created := future.Add(time.Duration(i) * time.Hour)
				if i == 3 {
					created = now.Add(-time.Hour)
				}
				m := Message{ID: int64(i + 1), SenderID: 1, RecipientID: 2, Content: fmt.Sprintf("clock fixture %d", i), Nonce: fmt.Sprintf("clock-fixture-nonce-%d", i), CreatedAt: created}
				if i == 1 {
					m.SenderID = 3
				}
				if i == 2 {
					m.RecipientID = 4
				}
				if i == 4 {
					at := created.Add(time.Hour)
					m.ReadAt = &at
				}
				state.Messages = append(state.Messages, m)
				state.Notices = append(state.Notices, Notice{ID: m.ID, MessageID: m.ID, UserID: m.RecipientID, PeerID: m.SenderID, CreatedAt: m.CreatedAt, ReadAt: m.ReadAt})
			}
			if err := validateDocument(state); err != nil {
				t.Fatal("fixture must be a valid restored store", err)
			}
			path := filepath.Join(t.TempDir(), "messages.json")
			raw, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			store, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			apply := func() error {
				if action == "conversation" {
					return store.Read(t.Context(), 2, 1, 6)
				}
				return store.UpdateNotices(t.Context(), 2, 6, action)
			}
			started := time.Now().UTC()
			if err := apply(); err != nil {
				t.Fatal("read should remain available after clock rollback", err)
			}
			wantMessages, wantNotices := map[int]bool{4: true}, map[int]bool{4: true}
			if action == "conversation" {
				for _, i := range []int{0, 3, 5} {
					wantMessages[i], wantNotices[i] = true, true
				}
			} else if action == "read" {
				wantNotices[5] = true
			} else {
				for _, i := range []int{0, 1, 3, 5} {
					wantNotices[i] = true
				}
			}
			check := func(kind string, i int, created time.Time, at *time.Time, want bool) {
				t.Helper()
				if !created.Equal(state.Messages[i].CreatedAt) {
					t.Errorf("%s %d changed creation time", kind, i)
				}
				if (at != nil) != want {
					t.Errorf("%s %d changed a different account, thread or cursor boundary", kind, i)
					return
				}
				if at == nil {
					return
				}
				if i == 4 {
					if !at.Equal(*state.Messages[i].ReadAt) {
						t.Errorf("%s replaced an existing read receipt", kind)
					}
				} else if created.After(started) {
					if !at.Equal(created) {
						t.Errorf("%s read receipt predates its message: created=%s read=%s", kind, created, at)
					}
				} else if at.Before(started) {
					t.Errorf("%s backdated a normal message's new read receipt", kind)
				}
			}
			for i, m := range store.data.Messages {
				check("message", i, m.CreatedAt, m.ReadAt, wantMessages[i])
				n := store.data.Notices[i]
				check("notice", i, n.CreatedAt, n.ReadAt, wantNotices[i])
			}
			store, err = NewFileStore(path)
			if err != nil {
				t.Fatal("read made the previously valid store unreadable", err)
			}
			beforeReplay, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := apply(); err != nil {
				t.Fatal(err)
			}
			afterReplay, err := os.ReadFile(path)
			if err != nil || string(afterReplay) != string(beforeReplay) {
				t.Fatal("repeated read changed existing receipts", err)
			}
		})
	}
}
