package notification

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
)

func notificationStorageFixture(t *testing.T, persistent bool) (*FileStore, string, time.Time, Delivery) {
	t.Helper()
	path := ""
	if persistent {
		path = filepath.Join(t.TempDir(), "notifications.json")
	}
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	prefs := Defaults(2)
	prefs.Email = true
	if _, err := store.SavePreferences(t.Context(), prefs, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := activity.Event{
		Key: "community:storage:1", Kind: activity.Community, Circle: "storage",
		UserID: 1, Version: 1, Status: "reply", TopicID: 1, ReplyID: 1,
		Mentions: []string{"reader"}, Recipients: []int64{2}, At: now, Due: now,
	}
	return store, path, now, Delivery{UserID: 2, Kind: Reply, Event: event}
}

func requireStoredNotificationEvent(t *testing.T, store *FileStore) {
	t.Helper()
	page, err := store.Notices(t.Context(), 2, Query{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("inbox = %#v, %v", page, err)
	}
	event := page.Items[0].Event
	if len(event.Mentions) != 1 || event.Mentions[0] != "reader" || len(event.Recipients) != 1 || event.Recipients[0] != 2 {
		t.Errorf("caller changed the stored event: mentions=%v recipients=%v", event.Mentions, event.Recipients)
	}
}

func TestProjectionOwnsEventSlices(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "file"}[persistent], func(t *testing.T) {
			store, path, now, delivery := notificationStorageFixture(t, persistent)
			if err := store.Project(t.Context(), delivery.Event.Key, []Delivery{delivery}, now); err != nil {
				t.Fatal(err)
			}
			delivery.Event.Mentions[0] = "changed"
			delivery.Event.Recipients[0] = 999
			requireStoredNotificationEvent(t, store)
			// A later write must not persist mutations made to the caller's input.
			if err := store.UpdateNotices(t.Context(), 2, 1, "read", now); err != nil {
				t.Fatal(err)
			}
			if persistent {
				reloaded, err := NewFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				requireStoredNotificationEvent(t, reloaded)
			}
		})
	}
}

func TestClaimMailReturnsIndependentEventAndLease(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "file"}[persistent], func(t *testing.T) {
			store, path, now, delivery := notificationStorageFixture(t, persistent)
			if err := store.Project(t.Context(), delivery.Event.Key, []Delivery{delivery}, now); err != nil {
				t.Fatal(err)
			}
			job, ok, err := store.ClaimMail(t.Context(), now)
			if err != nil || !ok || job.LeaseUntil == nil {
				t.Fatalf("claim = %#v, %v, %v", job, ok, err)
			}
			job.Event.Mentions[0] = "changed"
			job.Event.Recipients[0] = 999
			*job.LeaseUntil = now.Add(time.Hour)
			if _, claimed, err := store.ClaimMail(t.Context(), now.Add(2*time.Minute)); err != nil || claimed {
				t.Fatalf("recovery unexpectedly claimed mail: %v, %v", claimed, err)
			}
			status, err := store.MailStatus(t.Context(), 2)
			if err != nil || status.Uncertain != 1 || status.Pending != 0 {
				t.Errorf("caller changed the stored lease: status=%#v, err=%v", status, err)
			}
			// Finish recovery with a normal write so memory and disk are compared.
			if err := store.UpdateNotices(t.Context(), 2, 1, "read", now); err != nil {
				t.Fatal(err)
			}
			if persistent {
				store, err = NewFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			requireStoredNotificationEvent(t, store)
			// Returned slices must not replace the pending delivery on a later read.
			stored := store.state.Jobs[job.Key]
			if stored.Event.Mentions[0] != "reader" || stored.Event.Recipients[0] != 2 {
				t.Errorf("caller changed the queued delivery: %#v", stored.Event)
			}
		})
	}
}

func TestMaximumReceiptCanReloadAndReplayMail(t *testing.T) {
	store, path, now, delivery := notificationStorageFixture(t, true)
	receipt := strings.Repeat("r", 160)
	if err := store.Project(t.Context(), receipt, []Delivery{delivery}, now); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("accepted receipt made the notification store unreadable: %v", err)
	}
	if err := reloaded.Project(t.Context(), receipt, []Delivery{delivery}, now); err != nil {
		t.Fatal(err)
	}
	page, err := reloaded.Notices(t.Context(), 2, Query{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("replayed projection = %#v, %v", page, err)
	}
	job, ok, err := reloaded.ClaimMail(t.Context(), now)
	if err != nil || !ok || len(job.Key) > 160 {
		t.Fatalf("mail claim exceeds the database key limit: %#v, %v, %v", job, ok, err)
	}
	if err := reloaded.FinishMail(t.Context(), job, "sent", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := reloaded.ClaimMail(t.Context(), now.Add(time.Hour)); err != nil || ok {
		t.Fatalf("completed mail was replayed: %v, %v", ok, err)
	}
}

func TestMailJobKeysPreserveExistingKeysAndBoundLongReceipts(t *testing.T) {
	delivery := Delivery{UserID: 2, Kind: Reply}
	if got := JobKey("event:1", delivery); got != "event:1:reply:2" {
		t.Fatalf("existing task identity changed: %q", got)
	}
	boundaryReceipt := strings.Repeat("r", 160-len(":reply:2"))
	if got := JobKey(boundaryReceipt, delivery); got != boundaryReceipt+":reply:2" {
		t.Fatalf("valid 160-byte task identity changed: %q", got)
	}
	receipt := strings.Repeat("r", 160)
	keys := map[string]bool{}
	for _, kind := range []string{Reply, Mention, Publication, Moderation} {
		for _, user := range []int64{1, math.MaxInt64} {
			for _, prefix := range []string{"a", "b"} {
				input := prefix + receipt[1:]
				d := Delivery{UserID: user, Kind: kind}
				key := JobKey(input, d)
				if len(key) > 160 || key == "" || keys[key] || key != JobKey(input, d) {
					t.Errorf("invalid or colliding mail task key: %q", key)
				}
				keys[key] = true
			}
		}
	}
}

func TestLegacyOversizedMailKeyRecoveryPreservesIdentity(t *testing.T) {
	for _, state := range []string{"pending", "sending", "sent", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			store, path, now, delivery := notificationStorageFixture(t, true)
			receipt := strings.Repeat("r", 160)
			if err := store.Project(t.Context(), receipt, []Delivery{delivery}, now); err != nil {
				t.Fatal(err)
			}
			// Recreate exactly the oversized task key emitted by earlier versions.
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var old document
			if err := json.Unmarshal(raw, &old); err != nil {
				t.Fatal(err)
			}
			key := JobKey(receipt, delivery)
			job := old.Jobs[key]
			delete(old.Jobs, key)
			job.Key = fmt.Sprintf("%s:%s:%d", receipt, delivery.Kind, delivery.UserID)
			job.State = state
			if state == "sending" {
				lease := now.Add(-time.Minute)
				job.LeaseUntil = &lease
				job.Attempts = 1
			}
			old.Jobs[job.Key] = job
			raw, err = json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			reloaded, err := NewFileStore(path)
			if err != nil {
				t.Fatalf("valid legacy task could not be recovered: %v", err)
			}
			if err := reloaded.Project(t.Context(), receipt, []Delivery{delivery}, now); err != nil {
				t.Fatal(err)
			}
			claimed, ok, err := reloaded.ClaimMail(t.Context(), now)
			if err != nil || ok != (state == "pending") {
				t.Fatalf("legacy %s task replay: claimed=%v, error=%v", state, ok, err)
			}
			if ok {
				if claimed.Key != job.Key {
					t.Fatal("legacy delivery identity changed")
				}
				if err := reloaded.FinishMail(t.Context(), claimed, "sent", now); err != nil {
					t.Fatal(err)
				}
			}
			status, err := reloaded.MailStatus(t.Context(), 2)
			wantUncertain := 0
			if state == "sending" || state == "uncertain" {
				wantUncertain = 1
			}
			if err != nil || status.Pending != 0 || status.Uncertain != wantUncertain {
				t.Fatalf("recovered status = %#v, %v", status, err)
			}
			if _, err := NewFileStore(path); err != nil {
				t.Fatalf("recovered task cannot reload: %v", err)
			}
			// Only the exact old encoding backed by a valid receipt is accepted.
			delete(old.Receipts, receipt)
			if err := validateDocument(old); err == nil {
				t.Fatal("oversized task without its original receipt was accepted")
			}
		})
	}
}
