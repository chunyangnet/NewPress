package database

import (
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/notification"
)

func TestPostgreSQLNotificationClaimCommitFailure(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var user int64
	if err := pool.QueryRow(ctx, `INSERT INTO np_users(username,email,password_hash) VALUES('claim-commit-reader','claim-commit@example.test','unused-test-hash') RETURNING id`).Scan(&user); err != nil {
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
	if err := store.Project(ctx, event.Key, []notification.Delivery{delivery}, now); err != nil {
		t.Fatal(err)
	}
	// Defer failure until COMMIT so SELECT and the lease UPDATE both succeed.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION np_test_reject_mail_claim() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test claim commit failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE CONSTRAINT TRIGGER np_test_mail_claim_commit AFTER UPDATE ON np_notification_mail DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.state='sending') EXECUTE FUNCTION np_test_reject_mail_claim()`); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := store.ClaimMail(ctx, now)
	if err == nil || ok || claim.Key != "" || claim.LeaseUntil != nil {
		t.Errorf("failed COMMIT exposed a mail lease: claim=%+v claimed=%v error=%v", claim, ok, err)
	}
	var state string
	var attempts int
	var lease *time.Time
	if err := pool.QueryRow(ctx, `SELECT state,attempts,lease_until FROM np_notification_mail WHERE key=$1`, notification.JobKey(event.Key, delivery)).Scan(&state, &attempts, &lease); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || attempts != 0 || lease != nil {
		t.Fatal("failed COMMIT changed the persisted claim", state, attempts, lease)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER np_test_mail_claim_commit ON np_notification_mail`); err != nil {
		t.Fatal(err)
	}
	claim, ok, err = store.ClaimMail(ctx, now)
	if err != nil || !ok || claim.Attempts != 1 {
		t.Fatal("task was lost after a rejected commit", claim, ok, err)
	}
	if err := store.FinishMail(ctx, claim, "sent", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}
