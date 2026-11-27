package messaging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func messagingCapacityFixture(t *testing.T, spare, unread int) (*FileStore, string, []byte, Input) {
	t.Helper()
	created := time.Date(2026, 11, 20, 0, 0, 0, 123456789, time.UTC)
	read := created.Add(time.Hour)
	state := document{NextID: 1, Messages: []Message{}, Notices: []Notice{}}
	for i := range unread {
		m := Message{ID: state.NextID, SenderID: 1, RecipientID: 2, Content: "older unread", Nonce: fmt.Sprintf("old-capacity-nonce-%d", i), CreatedAt: created}
		state.Messages = append(state.Messages, m)
		state.Notices = append(state.Notices, Notice{ID: m.ID, UserID: 2, PeerID: 1, MessageID: m.ID, CreatedAt: created})
		state.NextID++
	}
	input := Input{SenderID: 1, RecipientID: 2, Content: "capacity arrival", Nonce: "capacity-arrival-nonce"}
	// Filling 64 MiB with 2,000-rune escaped messages yields four-digit IDs.
	// Use a maximal UTC timestamp; Send can only consume the same or fewer bytes.
	m := Message{ID: 9999, SenderID: 1, RecipientID: 2, Content: input.Content, Nonce: input.Nonce, CreatedAt: created}
	n := Notice{ID: m.ID, UserID: 2, PeerID: 1, MessageID: m.ID, CreatedAt: created}
	messageJSON, _ := json.Marshal(m)
	noticeJSON, _ := json.Marshal(n)
	admissionBytes := len(messageJSON) + len(noticeJSON) + 1
	if unread > 0 {
		admissionBytes++
	}
	target := maxDocumentBytes - spare - admissionBytes
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	size := len(raw)
	for size < target {
		m := Message{ID: state.NextID, SenderID: 3, RecipientID: 4, Nonce: fmt.Sprintf("capacity-filler-%08d", state.NextID), CreatedAt: created, ReadAt: &read}
		encoded, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		overhead := len(encoded) + len(strconv.FormatInt(m.ID+1, 10)) - len(strconv.FormatInt(m.ID, 10))
		if len(state.Messages) > 0 {
			overhead++
		}
		remaining := target - size
		added := min(remaining, overhead+6*(MaxContentRunes-5))
		if remaining > added && remaining-added < 512 {
			added = remaining - 512
		}
		padding := added - overhead
		if padding < 1 {
			t.Fatal("capacity fixture has insufficient room for final message")
		}
		m.Content = strings.Repeat("<", padding/6) + strings.Repeat("x", padding%6)
		state.Messages = append(state.Messages, m)
		state.NextID++
		size += added
	}
	if state.NextID < 1000 || state.NextID >= 9999 {
		t.Fatal("capacity fixture no longer has four-digit IDs", state.NextID)
	}
	if err := validateDocument(state); err != nil {
		t.Fatal("invalid capacity fixture", err)
	}
	raw, err = json.Marshal(state)
	if err != nil || len(raw) != target {
		t.Fatalf("fixture bytes=%d want=%d error=%v", len(raw), target, err)
	}
	path := filepath.Join(t.TempDir(), "messages.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return store, path, raw, input
}

func TestFileStoreCapacityRejectsUnreadableAdmission(t *testing.T) {
	for _, fixture := range []struct {
		name          string
		spare, unread int
	}{
		{"new message receipts", 1, 0},
		{"existing unread receipts", 128, 4},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			store, path, before, input := messagingCapacityFixture(t, fixture.spare, fixture.unread)
			nextID := store.data.NextID
			m, err := store.Send(t.Context(), input)
			if err == nil {
				t.Error("accepted a message without space for unread receipts")
				t.Log("read after admission:", store.Read(t.Context(), 2, 1, m.ID))
			}
			if m.ID != 0 || store.data.NextID != nextID {
				t.Error("rejected admission exposed or consumed an ID")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatal("rejected admission changed the durable file", err)
			}
			if fixture.unread > 0 {
				if err := store.Read(t.Context(), 2, 1, int64(fixture.unread)); err != nil {
					t.Fatal("rejected admission consumed older messages' read space", err)
				}
			}
			if _, err := NewFileStore(path); err != nil {
				t.Fatal("rejected admission made store unreadable", err)
			}
		})
	}
}

func TestFileStoreCapacityPreservesReadAndReplay(t *testing.T) {
	store, path, _, input := messagingCapacityFixture(t, 80, 0)
	m, err := store.Send(t.Context(), input)
	if err != nil {
		t.Fatal("rejected message with receipt space", err)
	}
	store, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.Send(t.Context(), input)
	if err != nil || replay.ID != m.ID {
		t.Fatal("replay reserved receipt space twice", replay, err)
	}
	if err := store.UpdateNotices(t.Context(), 2, m.ID, "read"); err != nil {
		t.Fatal("single notification read exceeded capacity", err)
	}
	if err := store.UpdateNotices(t.Context(), 2, m.ID, "read_all"); err != nil {
		t.Fatal("notification replay exceeded capacity", err)
	}
	if err := store.Read(t.Context(), 2, 1, m.ID); err != nil {
		t.Fatal("conversation read exceeded capacity", err)
	}
	store, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.Messages(t.Context(), 2, 1, MessageQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ReadAt == nil {
		t.Fatal("read receipt did not survive reload", page, err)
	}
	if err := store.UpdateNotices(t.Context(), 2, m.ID, "clear"); err != nil {
		t.Fatal("capacity guard blocked clearing notifications", err)
	}
	store, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	notices, err := store.Notices(t.Context(), 2, NoticeQuery{})
	if err != nil || len(notices.Items) != 0 {
		t.Fatal("cleared notification returned", notices, err)
	}
	page, err = store.Messages(t.Context(), 2, 1, MessageQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Content != input.Content {
		t.Fatal("clearing near-capacity notices lost the message", page, err)
	}
}
