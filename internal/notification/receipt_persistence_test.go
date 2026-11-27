package notification

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProjectionRejectsUnstableReceiptIdentity(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		for _, withDelivery := range []bool{false, true} {
			for _, test := range []struct {
				name, receipt string
				zeroTime      bool
			}{
				{name: "invalid-utf8", receipt: "receipt:\xff"},
				{name: "truncated-utf8", receipt: "receipt:\xe4\xb8"},
				{name: "expands-on-encode", receipt: strings.Repeat("r", 159) + "\xff"},
				{name: "nul", receipt: "receipt:\x00"},
				{name: "zero-time", receipt: "receipt:zero-time", zeroTime: true},
			} {
				name := map[bool]string{false: "memory/", true: "file/"}[persistent] + map[bool]string{false: "receipt-only/", true: "delivery/"}[withDelivery] + test.name
				t.Run(name, func(t *testing.T) {
					store, path, now, delivery := notificationStorageFixture(t, persistent)
					before, err := json.Marshal(store.state)
					if err != nil {
						t.Fatal(err)
					}
					var diskBefore []byte
					if persistent {
						diskBefore, err = os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
					}
					var deliveries []Delivery
					if withDelivery {
						deliveries = []Delivery{delivery}
					}
					if test.zeroTime {
						now = time.Time{}
					}
					if err := store.Project(t.Context(), test.receipt, deliveries, now); !errors.Is(err, ErrInvalid) {
						t.Errorf("unstable receipt was not rejected: %v", err)
					}
					after, err := json.Marshal(store.state)
					if err != nil || !bytes.Equal(before, after) {
						t.Error("rejected projection changed memory", err)
					}
					if persistent {
						after, err = os.ReadFile(path)
						if err != nil || !bytes.Equal(diskBefore, after) {
							t.Error("rejected projection changed the file", err)
						}
						if _, err := NewFileStore(path); err != nil {
							t.Error("projection made the store unreadable", err)
						}
					}
				})
			}
		}
	}
}

func TestUnicodeReceiptKeepsIdentityAfterReopen(t *testing.T) {
	store, path, now, delivery := notificationStorageFixture(t, true)
	receipt := strings.Repeat("中", 53) + "x"
	if len(receipt) != 160 {
		t.Fatal("fixture must exercise the byte limit")
	}
	if err := store.Project(t.Context(), receipt, []Delivery{delivery}, now); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Project(t.Context(), receipt, []Delivery{delivery}, now); err != nil {
		t.Fatal(err)
	}
	page, err := store.Notices(t.Context(), delivery.UserID, Query{})
	if err != nil || len(page.Items) != 1 || len(store.state.Receipts) != 1 {
		t.Fatal("replayed Unicode receipt duplicated delivery", page, err)
	}
	job, ok, err := store.ClaimMail(t.Context(), now)
	if err != nil || !ok || job.Key != JobKey(receipt, delivery) {
		t.Fatal("Unicode mail identity changed", job, ok, err)
	}
	if err := store.FinishMail(t.Context(), job, "sent", now); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path); err != nil {
		t.Fatal(err)
	}
}
