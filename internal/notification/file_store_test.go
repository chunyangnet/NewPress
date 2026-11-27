package notification

import (
	"context"
	"errors"
	"fmt"
	"newpress.local/newpress/internal/activity"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDurableProjectionAndMailRecovery(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "notifications.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p := Defaults(2)
	p.Email = true
	p, err = s.SavePreferences(ctx, p, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e := activity.New(activity.CommentPublic, 1, 1, 3, 1, "approved", now)
	d := Delivery{UserID: 2, Kind: Reply, Event: e}
	for i := 0; i < 3; i++ {
		if err = s.Project(ctx, e.Key, []Delivery{d, d}, now); err != nil {
			t.Fatal(err)
		}
	}
	s, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.Notices(ctx, 2, Query{})
	if err != nil || len(page.Items) != 1 || page.Unread != 1 {
		t.Fatal(page, err)
	}
	if err = s.UpdateNotices(ctx, 99, page.Through, "clear", now); err != nil {
		t.Fatal(err)
	}
	page, _ = s.Notices(ctx, 2, Query{})
	if len(page.Items) != 1 {
		t.Fatal("cross-account clear")
	}
	if err = s.UpdateNotices(ctx, 2, page.Through, "clear", now); err != nil {
		t.Fatal(err)
	}
	if err = s.Project(ctx, e.Key, []Delivery{d}, now); err != nil {
		t.Fatal(err)
	}
	page, _ = s.Notices(ctx, 2, Query{})
	if len(page.Items) != 0 {
		t.Fatal("cleared notification resurrected")
	}
	var wg sync.WaitGroup
	claimed := make(chan Job, 16)
	for i := 0; i < 16; i++ {
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
	jobs := []Job{}
	for j := range claimed {
		jobs = append(jobs, j)
	}
	if len(jobs) != 1 {
		t.Fatalf("claimed %d times", len(jobs))
	}
	s, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimMail(ctx, now.Add(2*time.Minute)); err != nil || ok {
		t.Fatal("expired sending job was replayed", err)
	}
	status, err := s.MailStatus(ctx, 2)
	if err != nil || status.Uncertain != 1 {
		t.Fatal(status, err)
	}
	if err = s.RetryFailed(ctx, 2, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ClaimMail(ctx, now.Add(time.Hour)); ok {
		t.Fatal("uncertain job was retried")
	}
	if err = s.FinishMail(ctx, jobs[0], "sent", now); !errors.Is(err, ErrConflict) {
		t.Fatal("late worker overwrite", err)
	}
	if err = s.Unsubscribe(ctx, 2, p.UnsubscribeKey, now); err != nil {
		t.Fatal(err)
	}
	newP, _ := s.Preferences(ctx, 2)
	newP.Email = true
	newP, err = s.SavePreferences(ctx, newP, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if ValidUnsubscribe(newP, UnsubscribeToken(p)) {
		t.Fatal("old unsubscribe token survived resubscribe")
	}
	if _, err = s.SavePreferences(ctx, p, now); !errors.Is(err, ErrConflict) {
		t.Fatal("stale preference save", err)
	}
}

func TestDefiniteFailuresBackoffAndAccountOptOut(t *testing.T) {
	s, _ := NewFileStore("")
	ctx := t.Context()
	now := time.Now().UTC()
	p := Defaults(1)
	p.Email = true
	_, err := s.SavePreferences(ctx, p, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e := activity.New(activity.CommentPublic, 1, 1, 2, 1, "approved", now)
	if err = s.Project(ctx, e.Key, []Delivery{{UserID: 1, Kind: Reply, Event: e}}, now); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 8; attempt++ {
		j, ok, err := s.ClaimMail(ctx, now)
		if err != nil || !ok || j.Attempts != attempt {
			t.Fatal(j, ok, err)
		}
		if err = s.FinishMail(ctx, j, "retry", now); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ = s.ClaimMail(ctx, now); ok {
			t.Fatal("retry skipped backoff")
		}
		now = now.Add(24 * time.Hour)
	}
	status, _ := s.MailStatus(ctx, 1)
	if status.Failed != 1 {
		t.Fatal(status)
	}
	if err = s.RetryFailed(ctx, 2, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ClaimMail(ctx, now); ok {
		t.Fatal("foreign user retried job")
	}
	if err = s.RetryFailed(ctx, 1, now); err != nil {
		t.Fatal(err)
	}
	j, ok, err := s.ClaimMail(ctx, now)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = s.FinishMail(ctx, j, "sent", now); err != nil {
		t.Fatal(err)
	}
	if err = s.RetryFailed(ctx, 1, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ = s.ClaimMail(ctx, now.Add(time.Hour)); ok {
		t.Fatal("successful mail resent")
	}
}

func TestFailedSaveDoesNotExposeCandidateAndStrictLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	s, _ := NewFileStore(path)
	ctx := t.Context()
	now := time.Now()
	p := Defaults(1)
	if _, err := s.SavePreferences(ctx, p, now); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(`{"bad":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Preferences(ctx, 1)
	p.Reply = false
	if _, err = s.SavePreferences(ctx, p, now); err == nil {
		t.Fatal("external overwrite accepted")
	}
	current, _ := s.Preferences(ctx, 1)
	if !current.Reply {
		t.Fatal("failed save exposed candidate")
	}
	if _, err = NewFileStore(path); err == nil {
		t.Fatal("corruption accepted")
	}
	if err = os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewFileStore(path); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = s.Project(cancelled, "never", nil, now); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestInboxCursorAndFiltersHaveIndependentBoundaries(t *testing.T) {
	s, _ := NewFileStore("")
	now := time.Now()
	for i := 1; i <= 73; i++ {
		e := activity.New(activity.CommentPublic, int64(i), 1, 3, 1, "approved", now)
		kind := Reply
		if i%2 == 0 {
			kind = Mention
		}
		if err := s.Project(t.Context(), fmt.Sprint(i), []Delivery{{UserID: 2, Kind: kind, Event: e}}, now); err != nil {
			t.Fatal(err)
		}
	}
	page, _ := s.Notices(t.Context(), 2, Query{})
	if len(page.Items) != 50 || !page.HasMore || page.Through != 73 || page.Unread != 73 {
		t.Fatal(page)
	}
	second, _ := s.Notices(t.Context(), 2, Query{Before: page.Items[49].ID})
	if len(second.Items) != 23 || second.HasMore || second.Through != 73 {
		t.Fatal(second)
	}
	if err := s.UpdateNotices(t.Context(), 2, 50, "read_all", now); err != nil {
		t.Fatal(err)
	}
	unread, _ := s.Notices(t.Context(), 2, Query{UnreadOnly: true, Kind: Mention})
	if unread.Unread != 23 || len(unread.Items) != 11 || unread.Through != 73 {
		t.Fatal(unread)
	}
}

func TestOptOutCancelsQueuedCategoryAcrossResubscribe(t *testing.T) {
	s, _ := NewFileStore("")
	ctx := t.Context()
	now := time.Now()
	p := Defaults(1)
	p.Email = true
	p, err := s.SavePreferences(ctx, p, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e := activity.New(activity.CommentPublic, 2, 1, 3, 1, "approved", now)
	if err = s.Project(ctx, e.Key, []Delivery{{UserID: 1, Kind: Reply, Event: e}}, now); err != nil {
		t.Fatal(err)
	}
	p.Reply = false
	p, err = s.SavePreferences(ctx, p, now)
	if err != nil {
		t.Fatal(err)
	}
	p.Reply = true
	if _, err = s.SavePreferences(ctx, p, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimMail(ctx, now.Add(time.Minute)); err != nil || ok {
		t.Fatal("opted-out job resurrected after enabling", err)
	}
}
