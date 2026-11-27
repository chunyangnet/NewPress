package database

import (
	"errors"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/notification"
)

func TestPostgreSQLNotificationReceiptIdentity(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var user int64
	if err := pool.QueryRow(ctx, `INSERT INTO np_users(username,email,password_hash) VALUES('receipt-reader','receipt@example.test','unused-test-hash') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	store := NewNotificationRepository(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	prefs := notification.Defaults(user)
	prefs.Email = true
	if _, err := store.SavePreferences(ctx, prefs, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := activity.New(activity.CommentPublic, 1, 1, 0, 1, "approved", now)
	delivery := notification.Delivery{UserID: user, Kind: notification.Reply, Event: event}
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
			name := map[bool]string{false: "receipt-only/", true: "delivery/"}[withDelivery] + test.name
			t.Run(name, func(t *testing.T) {
				at := now
				if test.zeroTime {
					at = time.Time{}
				}
				var deliveries []notification.Delivery
				if withDelivery {
					deliveries = []notification.Delivery{delivery}
				}
				if err := store.Project(ctx, test.receipt, deliveries, at); !errors.Is(err, notification.ErrInvalid) {
					t.Errorf("invalid projection did not return ErrInvalid: %v", err)
				}
				var receipts, notices, jobs int
				if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_notification_receipts), (SELECT count(*) FROM np_notifications), (SELECT count(*) FROM np_notification_mail)`).Scan(&receipts, &notices, &jobs); err != nil {
					t.Fatal(err)
				}
				if receipts != 0 || notices != 0 || jobs != 0 {
					t.Errorf("rejected projection persisted rows: receipts=%d notices=%d jobs=%d", receipts, notices, jobs)
				}
			})
		}
	}
	receipt := strings.Repeat("中", 53) + "x"
	for range 2 {
		store = NewNotificationRepository(pool)
		if err := store.Project(ctx, receipt, []notification.Delivery{delivery}, now); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.Notices(ctx, user, notification.Query{})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("Unicode replay duplicated the notice", page, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM np_notification_receipts WHERE event_key=$1`, receipt).Scan(&count); err != nil || count != 1 {
		t.Fatal("Unicode receipt identity changed", count, err)
	}
	job, ok, err := store.ClaimMail(ctx, now)
	if err != nil || !ok || job.Key != notification.JobKey(receipt, delivery) {
		t.Fatal("Unicode mail identity changed", job, ok, err)
	}
	if err := store.FinishMail(ctx, job, "sent", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimMail(ctx, now.Add(time.Hour)); err != nil || ok {
		t.Fatal("Unicode replay duplicated mail", ok, err)
	}
}
