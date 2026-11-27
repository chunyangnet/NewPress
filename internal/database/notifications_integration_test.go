package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/notification"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPostgreSQLNotifications(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	users := NewUserRepository(pool)
	if err = users.EnsureRoles(ctx, auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("Notification integration test password")
	if err != nil {
		t.Fatal(err)
	}
	stamp := fmt.Sprintf("notifications-%d", time.Now().UnixNano())
	u, err := users.CreateUser(ctx, auth.UserMutation{Actor: "integration"}, auth.UserInput{Username: stamp, Email: stamp + "@example.test", DisplayName: "通知集成用户", Locale: "zh-CN", Status: auth.UserStatusActive, RoleSlugs: []string{"author"}}, hash)
	if err != nil {
		t.Fatal(err)
	}
	postIDs := []int64{}
	receiptKeys := []string{}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool.Exec(cleanup, `DELETE FROM np_outbox WHERE topic LIKE 'notification.%' AND (payload->>'user_id')::bigint=$1`, u.ID)
		pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=ANY($1::bigint[])`, postIDs)
		pool.Exec(cleanup, `DELETE FROM np_users WHERE id=$1`, u.ID)
		pool.Exec(cleanup, `DELETE FROM np_notification_receipts WHERE event_key=ANY($1::text[])`, receiptKeys)
	}()
	posts := NewPostRepository(pool)
	input := content.PostInput{Title: "发布和通知原子提交", Slug: stamp, AuthorID: u.ID, Status: content.StatusPublish}
	p, err := posts.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	postIDs = append(postIDs, p.ID)
	key := fmt.Sprintf("publication:%d:1", p.ID)
	var raw []byte
	if err = pool.QueryRow(ctx, `SELECT payload FROM np_outbox WHERE event_key=$1`, key).Scan(&raw); err != nil {
		t.Fatal("missing transactional event", err)
	}
	var event activity.Event
	if err = json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	input.Title = "发布后编辑不会丢失通知"
	input.PublishedAt = p.PublishedAt
	p, err = posts.Update(ctx, p.ID, content.Mutation{ExpectedVersion: p.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_outbox WHERE topic='notification.post' AND payload->>'kind'=$2 AND (payload->>'post_id')::bigint=$1`, p.ID, activity.Publication).Scan(&count); err != nil || count != 1 {
		t.Fatal("ordinary edit changed publication events", count, err)
	}
	var retained []byte
	if err = pool.QueryRow(ctx, `SELECT payload FROM np_outbox WHERE event_key=$1`, key).Scan(&retained); err != nil || !bytes.Equal(retained, raw) {
		t.Fatal("ordinary edit replaced the pending publication", err)
	}
	updateKey := fmt.Sprintf("%s:%d:%d", activity.ContentUpdated, p.ID, p.Version)
	if err = pool.QueryRow(ctx, `SELECT payload FROM np_outbox WHERE topic='notification.post' AND event_key=$1`, updateKey).Scan(&retained); err != nil {
		t.Fatal("missing transactional content update", err)
	}
	var update activity.Event
	if err = json.Unmarshal(retained, &update); err != nil {
		t.Fatal(err)
	}
	if err = update.Validate(); err != nil || update.Kind != activity.ContentUpdated || update.PostID != p.ID || update.UserID != u.ID || update.Version != p.Version {
		t.Fatal("invalid content update", update, err)
	}
	s := NewNotificationRepository(pool)
	now := time.Now().UTC()
	prefs := notification.Defaults(u.ID)
	prefs.Email = true
	prefs, err = s.SavePreferences(ctx, prefs, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// A valid maximum-size receipt must still fit the derived mail key column.
	receipt := stamp + strings.Repeat("r", 160-len(stamp))
	receiptKeys = append(receiptKeys, receipt)
	delivery := notification.Delivery{UserID: u.ID, Kind: notification.Publication, Event: event}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			if err := s.Project(ctx, receipt, []notification.Delivery{delivery}, now); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	s = NewNotificationRepository(pool)
	page, err := s.Notices(ctx, u.ID, notification.Query{})
	if err != nil || len(page.Items) != 1 || page.Unread != 1 {
		t.Fatal(page, err)
	}
	if err = s.UpdateNotices(ctx, u.ID, page.Through, "clear", now); err != nil {
		t.Fatal(err)
	}
	if err = s.Project(ctx, receipt, []notification.Delivery{delivery}, now); err != nil {
		t.Fatal(err)
	}
	page, _ = s.Notices(ctx, u.ID, notification.Query{})
	if len(page.Items) != 0 {
		t.Fatal("clear resurrected")
	}
	claimed := make(chan notification.Job, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			j, ok, err := s.ClaimMail(ctx, now)
			if err != nil {
				t.Error(err)
			}
			if ok {
				claimed <- j
			}
		})
	}
	wg.Wait()
	close(claimed)
	jobs := []notification.Job{}
	for j := range claimed {
		jobs = append(jobs, j)
	}
	if len(jobs) != 1 || jobs[0].UserID != u.ID {
		t.Fatal("duplicate claim", jobs)
	}
	if _, ok, err := s.ClaimMail(ctx, now.Add(2*time.Minute)); err != nil || ok {
		t.Fatal("expired lease replayed", err)
	}
	status, err := s.MailStatus(ctx, u.ID)
	if err != nil || status.Uncertain != 1 {
		t.Fatal(status, err)
	}
	if err = s.RetryFailed(ctx, u.ID, now); err != nil {
		t.Fatal(err)
	}
	status, _ = s.MailStatus(ctx, u.ID)
	if status.Uncertain != 1 {
		t.Fatal("uncertain job retried")
	}
	if err = s.Unsubscribe(ctx, u.ID, prefs.UnsubscribeKey, now); err != nil {
		t.Fatal(err)
	}
	current, err := s.Preferences(ctx, u.ID)
	if err != nil || current.Email || current.Weekly {
		t.Fatal(current, err)
	}
	if _, err = s.SavePreferences(ctx, prefs, now); !errors.Is(err, notification.ErrConflict) {
		t.Fatal("stale preference update", err)
	}
	comments := NewCommentRepository(pool)
	c, err := comments.Create(ctx, comment.CreateInput{PostID: p.ID, UserID: u.ID, AuthorName: "通知用户", AuthorEmail: u.Email, ContentSource: "@someone 等待审核", Status: comment.StatusPending})
	if err != nil {
		t.Fatal(err)
	}
	c, err = comments.ChangeStatus(ctx, c.ID, comment.Mutation{ExpectedVersion: c.Version, Actor: "integration"}, comment.StatusApproved)
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_outbox WHERE topic='notification.comment' AND (payload->>'object_id')::bigint=$1`, c.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("moderation event atomicity", count, err)
	}
	source := NewActivitySource(pool, "notification.post")
	if err = source.AckActivity(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT payload FROM np_outbox WHERE event_key=$1`, key).Scan(&raw); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("source ack failed", err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_outbox WHERE event_key=$1`, updateKey).Scan(&count); err != nil || count != 1 {
		t.Fatal("publication ACK removed the content update", count, err)
	}
}
