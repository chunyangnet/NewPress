package notification

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
	"newpress.local/newpress/internal/maildelivery"
)

type delayedMailClaimStore struct {
	Store
	delay time.Duration
}

func (s delayedMailClaimStore) ClaimMail(ctx context.Context, now time.Time) (Job, bool, error) {
	job, ok, err := s.Store.ClaimMail(ctx, now)
	if ok && err == nil {
		time.Sleep(s.delay)
	}
	return job, ok, err
}

type delayedMailUsers struct {
	Users
	delay time.Duration
}

func (u delayedMailUsers) GetUser(ctx context.Context, id int64) (auth.User, error) {
	time.Sleep(u.delay)
	return u.Users.GetUser(ctx, id)
}

type deadlineMailSender func(context.Context, maildelivery.Message) error

func (s deadlineMailSender) SendMessage(ctx context.Context, message maildelivery.Message) error {
	return s(ctx, message)
}

func TestDeliverySharesLeaseAcrossClaimPreparationAndSMTP(t *testing.T) {
	for _, tc := range []struct {
		name         string
		claim, users time.Duration
		expired      bool
		waitForLease bool
	}{
		{"slow-claim", 61 * time.Second, 0, true, false},
		{"slow-preparation", 0, 61 * time.Second, true, false},
		{"combined-delay", 31 * time.Second, 31 * time.Second, true, false},
		{"remaining-smtp-budget", 10 * time.Second, 10 * time.Second, false, false},
		{"smtp-timeout", 10 * time.Second, 10 * time.Second, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, posts, _, _ := notificationFixture(t)
				now := time.Now()
				enableMail(t, s.Store, 2, now.Add(-time.Hour))
				if err := s.Engagement.Set(t.Context(), engagement.Input{UserID: 2, TargetID: 1, Kind: engagement.Follow, Active: true}); err != nil {
					t.Fatal(err)
				}
				if _, err := posts.Create(t.Context(), content.PostInput{Title: "通知", Slug: "lease-budget", AuthorID: 1, Status: content.StatusPublish}); err != nil {
					t.Fatal(err)
				}
				s.Store = delayedMailClaimStore{Store: s.Store, delay: tc.claim}
				// Only delivery gets a delayed lookup; projection has already run.
				events, err := posts.PendingActivity(t.Context(), now, 50)
				if err != nil || len(events) != 1 {
					t.Fatal(events, err)
				}
				if err := s.project(t.Context(), events[0], now); err != nil {
					t.Fatal(err)
				}
				s.Sources = nil
				s.lastDigestCheck = now
				s.Users = delayedMailUsers{Users: s.Users, delay: tc.users}
				sent := 0
				s.Sender = deadlineMailSender(func(ctx context.Context, _ maildelivery.Message) error {
					sent++
					deadline, ok := ctx.Deadline()
					if !ok || !deadline.Equal(now.Add(time.Minute)) || ctx.Err() != nil {
						t.Errorf("SMTP did not receive the remaining lease: deadline=%v ok=%v error=%v", deadline, ok, ctx.Err())
					}
					if tc.waitForLease {
						select {
						case <-ctx.Done():
						case <-time.After(time.Minute):
							t.Error("SMTP was not cancelled when its lease expired")
						}
						if !time.Now().Equal(now.Add(time.Minute)) {
							t.Error("SMTP exceeded the remaining lease")
						}
						return maildelivery.ErrUncertain
					}
					return nil
				})
				err = s.Tick(t.Context(), now)
				if tc.expired || tc.waitForLease {
					wantSent := 0
					if tc.waitForLease {
						wantSent = 1
					}
					if sent != wantSent || !errors.Is(err, ErrConflict) {
						t.Fatalf("invalid expired delivery result: sent=%d want=%d error=%v", sent, wantSent, err)
					}
					if _, ok, err := s.Store.ClaimMail(t.Context(), time.Now()); err != nil || ok {
						t.Fatalf("expired delivery was resent: %v %v", ok, err)
					}
					status, err := s.Store.MailStatus(t.Context(), 2)
					if err != nil || status != (MailStatus{Uncertain: 1}) {
						t.Fatalf("expired delivery status = %+v, %v", status, err)
					}
				} else if err != nil || sent != 1 {
					t.Fatalf("valid lease did not send once: sent=%d error=%v", sent, err)
				}
			})
		})
	}
}
