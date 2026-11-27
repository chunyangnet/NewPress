package database

import (
	"errors"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/notification"
)

func TestPostgreSQLNotificationMailClaimFencing(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var user int64
	if err := pool.QueryRow(ctx, `INSERT INTO np_users(username,email,password_hash) VALUES('mail-claim-reader','mail-claim@example.test','unused-test-hash') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	store := NewNotificationRepository(pool)
	base := time.Now().UTC().Truncate(time.Microsecond)
	prefs := notification.Defaults(user)
	prefs.Email = true
	if _, err := store.SavePreferences(ctx, prefs, base.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var eventID int64
	for _, expired := range []bool{false, true} {
		for _, outcome := range []string{"sent", "retry", "uncertain", "cancelled"} {
			name := "manual-retry/" + outcome
			if expired {
				name = "expired/" + outcome
			}
			t.Run(name, func(t *testing.T) {
				// Each case owns the only mail row in this isolated schema.
				if _, err := pool.Exec(ctx, `DELETE FROM np_notification_mail`); err != nil {
					t.Fatal(err)
				}
				eventID++
				now := base
				event := activity.New(activity.CommentPublic, eventID, 1, 0, 1, "approved", now)
				delivery := notification.Delivery{UserID: user, Kind: notification.Reply, Event: event}
				if err := store.Project(ctx, event.Key, []notification.Delivery{delivery}, now); err != nil {
					t.Fatal(err)
				}
				old, ok, err := store.ClaimMail(ctx, now)
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
				if expired {
					if err := store.FinishMail(ctx, old, outcome, *old.LeaseUntil); !errors.Is(err, notification.ErrConflict) {
						t.Fatalf("expired %s completion accepted: %v", outcome, err)
					}
					if _, ok, err := store.ClaimMail(ctx, now.Add(2*time.Minute)); err != nil || ok {
						t.Fatal("expired claim was resent", ok, err)
					}
					status, err := store.MailStatus(ctx, user)
					if err != nil || status != (notification.MailStatus{Uncertain: 1}) {
						t.Fatalf("expired claim lost uncertain state: %+v %v", status, err)
					}
					return
				}
				claim := old
				for attempt := 1; attempt <= 8; attempt++ {
					if claim.Attempts != attempt {
						t.Fatalf("attempt=%d claim=%+v", attempt, claim)
					}
					if err := store.FinishMail(ctx, claim, "retry", now.Add(time.Second)); err != nil {
						t.Fatal(err)
					}
					now = now.Add(24 * time.Hour)
					if attempt < 8 {
						claim, ok, err = store.ClaimMail(ctx, now)
						if err != nil || !ok {
							t.Fatal(ok, err)
						}
					}
				}
				if err := store.RetryFailed(ctx, user, now); err != nil {
					t.Fatal(err)
				}
				current, ok, err := store.ClaimMail(ctx, now)
				if err != nil || !ok || current.Attempts != old.Attempts {
					t.Fatalf("manual retry did not reuse the attempt count: %+v %v %v", current, ok, err)
				}
				for _, completedAt := range []time.Time{old.UpdatedAt.Add(time.Second), now.Add(time.Second)} {
					if err := store.FinishMail(ctx, old, outcome, completedAt); !errors.Is(err, notification.ErrConflict) {
						t.Fatalf("old %s completion changed current claim: %v", outcome, err)
					}
				}
				if err := store.FinishMail(ctx, current, "sent", now.Add(2*time.Second)); err != nil {
					t.Fatalf("current claim could not complete: %v", err)
				}
			})
		}
	}
	for _, name := range []string{"state", "attempt", "missing-lease", "lease", "claimed-at", "before-claim"} {
		t.Run("invalid/"+name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `DELETE FROM np_notification_mail`); err != nil {
				t.Fatal(err)
			}
			eventID++
			event := activity.New(activity.CommentPublic, eventID, 1, 0, 1, "approved", base)
			delivery := notification.Delivery{UserID: user, Kind: notification.Reply, Event: event}
			if err := store.Project(ctx, event.Key, []notification.Delivery{delivery}, base); err != nil {
				t.Fatal(err)
			}
			current, ok, err := store.ClaimMail(ctx, base)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			claim, completedAt := current, base.Add(2*time.Second)
			switch name {
			case "state":
				claim.State = "pending"
			case "attempt":
				claim.Attempts++
			case "missing-lease":
				claim.LeaseUntil = nil
			case "lease":
				lease := claim.LeaseUntil.Add(time.Second)
				claim.LeaseUntil = &lease
			case "claimed-at":
				claim.UpdatedAt = claim.UpdatedAt.Add(time.Second)
			case "before-claim":
				completedAt = base.Add(-time.Second)
			}
			if err := store.FinishMail(ctx, claim, "sent", completedAt); !errors.Is(err, notification.ErrConflict) {
				t.Fatalf("invalid claim completed: %v", err)
			}
			zone := time.FixedZone("reader", 8*60*60)
			current.UpdatedAt = current.UpdatedAt.In(zone)
			lease := current.LeaseUntil.In(zone)
			current.LeaseUntil = &lease
			if err := store.FinishMail(ctx, current, "sent", base.Add(3*time.Second)); err != nil {
				t.Fatalf("valid claim could not complete: %v", err)
			}
		})
	}
	t.Run("nanosecond-clock", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `DELETE FROM np_notification_mail`); err != nil {
			t.Fatal(err)
		}
		eventID++
		now := base.Add(123 * time.Nanosecond)
		event := activity.New(activity.CommentPublic, eventID, 1, 0, 1, "approved", now)
		if err := store.Project(ctx, event.Key, []notification.Delivery{{UserID: user, Kind: notification.Reply, Event: event}}, now); err != nil {
			t.Fatal(err)
		}
		claim, ok, err := store.ClaimMail(ctx, now)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		if err := store.FinishMail(ctx, claim, "sent", now.Add(time.Second)); err != nil {
			t.Fatalf("timestamp precision prevented a current claim from completing: %v", err)
		}
	})
}
