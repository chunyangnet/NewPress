package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func sendFixture(t *testing.T, store Store, from, to int64, index int) Message {
	t.Helper()
	m, err := store.Send(t.Context(), Input{SenderID: from, RecipientID: to, Content: fmt.Sprintf("你好 [微笑] %d", index), Nonce: fmt.Sprintf("fixture-nonce-%020d", index)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFileStoreConversationLifecycleAndDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first := sendFixture(t, store, 1, 2, 1)
	replay, err := store.Send(t.Context(), Input{SenderID: 1, RecipientID: 2, Content: first.Content, Nonce: first.Nonce})
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v %v", replay, err)
	}
	if _, err := store.Send(t.Context(), Input{SenderID: 1, RecipientID: 3, Content: first.Content, Nonce: first.Nonce}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed recipient = %v", err)
	}
	unrelated := sendFixture(t, store, 3, 4, 2)
	inbox, err := store.Conversations(t.Context(), 2, 0)
	if err != nil || inbox.Unread != 1 || len(inbox.Items) != 1 || inbox.Items[0].PeerID != 1 {
		t.Fatalf("inbox = %+v %v", inbox, err)
	}
	notices, err := store.Notices(t.Context(), 2, NoticeQuery{})
	if err != nil || notices.Unread != 1 || notices.Through != first.ID {
		t.Fatalf("notices = %+v %v", notices, err)
	}
	if err := store.Read(t.Context(), 2, 1, unrelated.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign receipt = %v", err)
	}
	if err := store.Read(t.Context(), 2, 1, first.ID); err != nil {
		t.Fatal(err)
	}
	page, err := store.Messages(t.Context(), 1, 2, MessageQuery{After: first.ID})
	if err != nil || len(page.Items) != 0 || page.ReadThrough != first.ID {
		t.Fatalf("receipt-only poll = %+v %v", page, err)
	}
	notices, _ = store.Notices(t.Context(), 2, NoticeQuery{UnreadOnly: true})
	if notices.Unread != 0 || len(notices.Items) != 0 {
		t.Fatalf("read notices = %+v", notices)
	}
	if err := store.UpdateNotices(t.Context(), 2, first.ID, "clear"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err = reloaded.Messages(t.Context(), 2, 1, MessageQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ReadAt == nil || page.Items[0].Content != first.Content {
		t.Fatalf("reload = %+v %v", page, err)
	}
	*page.Items[0].ReadAt = time.Time{}
	page, _ = reloaded.Messages(t.Context(), 2, 1, MessageQuery{})
	if page.Items[0].ReadAt.IsZero() {
		t.Fatal("read page leaked a mutable pointer")
	}
	notices, _ = reloaded.Notices(t.Context(), 2, NoticeQuery{})
	if len(notices.Items) != 0 {
		t.Fatal("cleared notices reappeared")
	}
	foreign, _ := reloaded.Messages(t.Context(), 3, 2, MessageQuery{})
	if len(foreign.Items) != 0 {
		t.Fatal("private messages crossed participants")
	}
}

func TestFileStorePagingFilteringAndSnapshotBoundary(t *testing.T) {
	store, _ := NewFileStore("")
	for i := 1; i <= 52; i++ {
		sendFixture(t, store, 1, 2, i)
	}
	page, _ := store.Messages(t.Context(), 2, 1, MessageQuery{})
	if !page.HasMore || len(page.Items) != 50 || page.Items[0].ID != 52 || page.Items[49].ID != 3 {
		t.Fatalf("latest page = %+v", page)
	}
	older, _ := store.Messages(t.Context(), 2, 1, MessageQuery{Before: 3})
	if older.HasMore || len(older.Items) != 2 || older.Items[0].ID != 2 {
		t.Fatalf("older page = %+v", older)
	}
	newer, _ := store.Messages(t.Context(), 2, 1, MessageQuery{After: 1})
	if !newer.HasMore || len(newer.Items) != 50 || newer.Items[0].ID != 2 || newer.Items[49].ID != 51 {
		t.Fatalf("catch-up page = %+v", newer)
	}
	for _, action := range []string{"read_all", "clear"} {
		if err := store.UpdateNotices(t.Context(), 2, 50, action); err != nil {
			t.Fatal(err)
		}
		notices, _ := store.Notices(t.Context(), 2, NoticeQuery{UnreadOnly: true})
		if notices.Unread != 2 || len(notices.Items) != 2 || notices.Items[0].ID != 52 {
			t.Fatalf("snapshot %s lost new notices: %+v", action, notices)
		}
	}
	inbox, _ := store.Conversations(t.Context(), 2, 0)
	if inbox.Unread != 52 {
		t.Fatal("clearing notices changed message unread state")
	}
	for i := 3; i < 55; i++ {
		sendFixture(t, store, int64(i), 2, 100+i)
	}
	inbox, _ = store.Conversations(t.Context(), 2, 0)
	if !inbox.HasMore || len(inbox.Items) != 50 || inbox.Unread != 104 {
		t.Fatalf("bounded inbox = %+v", inbox)
	}
	tail, _ := store.Conversations(t.Context(), 2, inbox.Items[49].Last.ID)
	if tail.HasMore || len(tail.Items) != 3 || tail.Unread != 104 {
		t.Fatalf("inbox totals changed with page: %+v", tail)
	}
}

func TestFileStoreAtomicNonceAndRateLimit(t *testing.T) {
	store, _ := NewFileStore("")
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Go(func() {
			_, err := store.Send(t.Context(), Input{SenderID: 1, RecipientID: 2, Content: "并发重试", Nonce: "same-nonce-0000000000"})
			if err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	page, _ := store.Messages(t.Context(), 1, 2, MessageQuery{})
	if len(page.Items) != 1 {
		t.Fatalf("duplicate messages = %d", len(page.Items))
	}
	for i := 1; i < SendLimit; i++ {
		sendFixture(t, store, 1, 2, i)
	}
	if _, err := store.Send(t.Context(), Input{SenderID: 1, RecipientID: 2, Content: "超限", Nonce: "rate-limit-nonce-0000"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("limit = %v", err)
	}
	if _, err := store.Send(t.Context(), Input{SenderID: 1, RecipientID: 2, Content: "并发重试", Nonce: "same-nonce-0000000000"}); err != nil {
		t.Fatalf("replay incorrectly rate limited: %v", err)
	}
}

func TestFileStoreFailedWriteAndCancelledRequestLeaveNoMessage(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewFileStore(filepath.Join(dir, "messages.json"))
	if err := os.Mkdir(store.path, 0700); err != nil {
		t.Fatal(err)
	}
	input := Input{SenderID: 1, RecipientID: 2, Content: "不会丢失", Nonce: "atomic-nonce-00000000"}
	if _, err := store.Send(t.Context(), input); err == nil {
		t.Fatal("write unexpectedly succeeded over a directory")
	}
	page, _ := store.Messages(t.Context(), 1, 2, MessageQuery{})
	if len(page.Items) != 0 {
		t.Fatal("failed write changed memory")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Send(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestFileStoreRejectsCorruptFacts(t *testing.T) {
	store, _ := NewFileStore("")
	sendFixture(t, store, 1, 2, 1)
	for _, mutate := range []func(*document){
		func(d *document) { d.NextID = 1 },
		func(d *document) { d.Messages[0].SenderID = d.Messages[0].RecipientID },
		func(d *document) { d.Notices[0].UserID = 99 },
		func(d *document) { d.Messages = append(d.Messages, d.Messages[0]) },
	} {
		body, _ := json.Marshal(store.data)
		var d document
		json.Unmarshal(body, &d)
		mutate(&d)
		body, _ = json.Marshal(d)
		path := filepath.Join(t.TempDir(), "messages.json")
		os.WriteFile(path, body, 0600)
		if _, err := NewFileStore(path); err == nil {
			t.Fatal("corrupt document accepted")
		}
	}
}

func TestNormalizeMessageUnicodeAndHostileInput(t *testing.T) {
	valid := Input{SenderID: 1, RecipientID: 2, Content: strings.Repeat("😀", 2000), Nonce: "valid-nonce-00000000"}
	if _, err := Normalize(valid); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{" \r\n ", strings.Repeat("字", 2001), "unsafe\x00text", string([]byte{0xff})} {
		candidate := valid
		candidate.Content = body
		if _, err := Normalize(candidate); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad body accepted: %v", err)
		}
	}
	valid.Content = "  <script>alert(1)</script> [微笑]\r\n下一行  "
	result, err := Normalize(valid)
	if err != nil || result.Content != "<script>alert(1)</script> [微笑]\n下一行" {
		t.Fatalf("plain text normalization = %+v %v", result, err)
	}
}
