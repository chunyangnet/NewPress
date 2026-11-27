package notification

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
)

// Receipts are immutable and can legally occupy most of a notification file.
// Escaped ASCII keeps the fixture's map count small without relaxing key limits.
func fillNotificationCapacity(t *testing.T, state document, targetBytes int) (document, []byte) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	size := len(raw)
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	encodedTime, err := json.Marshal(now)
	if err != nil {
		t.Fatal(err)
	}
	const prefixBytes = 10
	for index := 0; size < targetBytes; index++ {
		prefix := fmt.Sprintf("f%08d:", index)
		if len(prefix) != prefixBytes {
			t.Fatal("too many filler receipts")
		}
		overhead := len(encodedTime) + 4 // key quotes, colon and comma
		if len(state.Receipts) == 0 {
			overhead--
		}
		minimum := overhead + prefixBytes
		remaining := targetBytes - size
		added := min(remaining, minimum+850)
		if remaining > added && remaining-added < minimum+1 {
			added = remaining - (minimum + 1)
		}
		if added < minimum {
			t.Fatal("capacity fixture leaves insufficient room for a receipt")
		}
		padding := added - minimum
		key := prefix + strings.Repeat("<", padding/6) + strings.Repeat("x", padding%6)
		if len(key) > 160 {
			t.Fatal("capacity fixture exceeds receipt byte limit")
		}
		state.Receipts[key] = now
		size += added
	}
	if err := validateDocument(state); err != nil {
		t.Fatal("invalid capacity fixture", err)
	}
	raw, err = json.Marshal(state)
	if err != nil || len(raw) != targetBytes {
		t.Fatalf("capacity fixture bytes=%d want=%d error=%v", len(raw), targetBytes, err)
	}
	return state, raw
}

func notificationCapacityPost(t *testing.T) (document, time.Time, string, Delivery) {
	t.Helper()
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	state := emptyDocument()
	input := Defaults(2)
	input.Email, input.Weekly = true, true
	prefs, err := UpdatedPreferences(Defaults(2), input, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	state.Preferences[2] = prefs
	event := activity.New(activity.CommentPublic, 1, 1, 3, 1, "approved", now)
	delivery := Delivery{UserID: 2, Kind: Reply, Event: event}
	return state, now, event.Key, delivery
}

func openNotificationCapacity(t *testing.T, raw []byte) (*FileStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notifications.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

func TestNotificationCapacityRejectsUnexecutableAdmission(t *testing.T) {
	state, now, receipt, delivery := notificationCapacityPost(t)
	state.Receipts[receipt] = now
	state.Notices = append(state.Notices, Notice{ID: state.NextID, Delivery: delivery, CreatedAt: now})
	state.NextID++
	key := JobKey(receipt, delivery)
	state.Jobs[key] = NewJob(key, delivery, now)
	state, _ = fillNotificationCapacity(t, state, maxFileBytes-1)
	delete(state.Receipts, receipt)
	delete(state.Jobs, key)
	state.Notices = nil
	state.NextID = 1
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	store, path := openNotificationCapacity(t, before)
	if err := store.Project(t.Context(), receipt, []Delivery{delivery}, now); err == nil {
		t.Error("accepted mail without space for the first lease")
		_, claimed, claimErr := store.ClaimMail(t.Context(), now)
		t.Logf("first claim after admission: claimed=%v error=%v", claimed, claimErr)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected admission changed the durable file", err)
	}
	page, err := store.Notices(t.Context(), delivery.UserID, Query{})
	if err != nil || len(page.Items) != 0 || len(store.state.Jobs) != 0 {
		t.Fatal("rejected admission changed memory", page, err)
	}
}

func TestNotificationCapacityProtectsFailedMailForRetry(t *testing.T) {
	state, now, receipt, delivery := notificationCapacityPost(t)
	state.Receipts[receipt] = now
	state.Notices = append(state.Notices, Notice{ID: state.NextID, Delivery: delivery, CreatedAt: now})
	state.NextID++
	key := JobKey(receipt, delivery)
	failed := NewJob(key, delivery, now)
	failed.State, failed.Attempts = "failed", 8
	state.Jobs[key] = failed
	incoming := strings.Repeat("<", 150)
	state.Receipts[incoming] = now
	state, _ = fillNotificationCapacity(t, state, maxFileBytes-1)
	delete(state.Receipts, incoming)
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	store, path := openNotificationCapacity(t, before)
	if err := store.Project(t.Context(), incoming, nil, now); err == nil {
		t.Error("receipt-only admission consumed an existing task's execution space")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected receipt changed the file", err)
	}
	// A replay needs no new space, including a receipt-only replay.
	if err := store.Project(t.Context(), receipt, []Delivery{delivery}, now); err != nil {
		t.Fatal("existing receipt could not be replayed", err)
	}
	if err := store.RetryFailed(t.Context(), delivery.UserID, now); err != nil {
		t.Fatal("failed task could not be scheduled for manual retry", err)
	}
	claim, ok, err := store.ClaimMail(t.Context(), now.Add(123456789*time.Nanosecond))
	if err != nil || !ok || claim.Attempts != 1 {
		t.Fatal("accepted job could not acquire its lease", ok, err)
	}
	if err := store.FinishMail(t.Context(), claim, "retry", now.Add(time.Second)); err != nil {
		t.Fatal("accepted job could not persist retry metadata", err)
	}
	now = now.Add(24 * time.Hour)
	claim, ok, err = store.ClaimMail(t.Context(), now)
	if err != nil || !ok || claim.Attempts != 2 {
		t.Fatal("accepted job could not be retried", claim, ok, err)
	}
	if err := store.FinishMail(t.Context(), claim, "sent", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateNotices(t.Context(), delivery.UserID, 1, "read_all", now); err != nil {
		t.Fatal("existing notices could not be marked read", err)
	}
	if err := store.UpdateNotices(t.Context(), delivery.UserID, 1, "clear", now); err != nil {
		t.Fatal("existing notices could not be cleared", err)
	}
	prefs, err := store.Preferences(t.Context(), delivery.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Unsubscribe(t.Context(), prefs.UserID, prefs.UnsubscribeKey, now); err != nil {
		t.Fatal("existing subscription could not be disabled", err)
	}
	if _, err := NewFileStore(path); err != nil {
		t.Fatal("capacity transitions made the store unreadable", err)
	}
}

func TestNotificationCapacityAdmitsWorkWithTransitionSpace(t *testing.T) {
	state, now, receipt, delivery := notificationCapacityPost(t)
	state.Receipts[receipt] = now
	state.Notices = append(state.Notices, Notice{ID: state.NextID, Delivery: delivery, CreatedAt: now})
	state.NextID++
	key := JobKey(receipt, delivery)
	state.Jobs[key] = NewJob(key, delivery, now)
	state, _ = fillNotificationCapacity(t, state, maxFileBytes-1024)
	delete(state.Receipts, receipt)
	delete(state.Jobs, key)
	state.Notices = nil
	state.NextID = 1
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	store, path := openNotificationCapacity(t, before)
	if err := store.Project(t.Context(), receipt, []Delivery{delivery}, now); err != nil {
		t.Fatal("space for execution was available", err)
	}
	store, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := store.ClaimMail(t.Context(), now.Add(123456789*time.Nanosecond))
	if err != nil || !ok {
		t.Fatal("newly admitted work cannot be claimed after reload", ok, err)
	}
	if err := store.FinishMail(t.Context(), claim, "sent", now.Add(time.Second)); err != nil {
		t.Fatal("newly admitted work cannot finish", err)
	}
	if err := store.UpdateNotices(t.Context(), delivery.UserID, 1, "read_all", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateNotices(t.Context(), delivery.UserID, 1, "clear", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	store, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Project(t.Context(), receipt, []Delivery{delivery}, now); err != nil {
		t.Fatal("cleared receipt did not remain replayable", err)
	}
	page, err := store.Notices(t.Context(), delivery.UserID, Query{})
	if err != nil || len(page.Items) != 0 || store.state.Jobs[key].State != "sent" {
		t.Fatal("replay resurrected cleared notice or completed mail", page, err)
	}
}

func TestNotificationCapacityCoversWeeklyAndPreferenceAdmissions(t *testing.T) {
	for _, kind := range []string{"weekly", "preferences"} {
		t.Run(kind, func(t *testing.T) {
			state, now, _, _ := notificationCapacityPost(t)
			end := now.Add(time.Hour)
			key := WeeklyKey(2, end)
			input := Defaults(3)
			if kind == "weekly" {
				state.Jobs[key] = NewJob(key, Delivery{UserID: 2, Kind: Weekly, Event: activity.Event{At: end.AddDate(0, 0, -7), Due: end}}, now)
			} else {
				prefs, err := UpdatedPreferences(Defaults(3), input, now)
				if err != nil {
					t.Fatal(err)
				}
				state.Preferences[3] = prefs
			}
			state, _ = fillNotificationCapacity(t, state, maxFileBytes-1)
			delete(state.Jobs, key)
			delete(state.Preferences, 3)
			store, err := NewFileStore("")
			if err != nil {
				t.Fatal(err)
			}
			store.state = state
			if kind == "weekly" {
				err = store.QueueWeekly(t.Context(), 2, end, now)
			} else {
				_, err = store.SavePreferences(t.Context(), input, now)
			}
			if err == nil || len(store.state.Jobs) != 0 || len(store.state.Preferences) != 1 {
				t.Fatal("admission consumed space reserved for future updates", err)
			}
		})
	}
}

func TestNotificationCapacityFailedClaimReturnsNoLease(t *testing.T) {
	state, now, receipt, delivery := notificationCapacityPost(t)
	state.Receipts[receipt] = now
	key := JobKey(receipt, delivery)
	state.Jobs[key] = NewJob(key, delivery, now)
	_, before := fillNotificationCapacity(t, state, maxFileBytes-1)
	// Legacy files may already be full. A failed claim must not expose an
	// uncommitted lease to callers, and must retain the original pending task.
	store, path := openNotificationCapacity(t, before)
	claim, ok, err := store.ClaimMail(t.Context(), now)
	if err == nil || ok || claim.Key != "" || claim.LeaseUntil != nil {
		t.Fatalf("failed claim exposed a lease: claim=%+v claimed=%v error=%v", claim, ok, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed claim changed persistent state", err)
	}
	if stored := store.state.Jobs[key]; stored.State != "pending" || stored.Attempts != 0 || stored.LeaseUntil != nil {
		t.Fatal("failed claim changed memory", stored)
	}
}
