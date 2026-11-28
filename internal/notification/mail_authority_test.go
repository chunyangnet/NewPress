package notification

import (
	"context"
	"errors"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
)

type mailAuthorityContent struct {
	content.Repository
	afterRead func()
}

func (r *mailAuthorityContent) changed() {
	if r.afterRead != nil {
		change := r.afterRead
		r.afterRead = nil
		change()
	}
}

func (r *mailAuthorityContent) GetByID(ctx context.Context, id int64) (content.Post, error) {
	post, err := r.Repository.GetByID(ctx, id)
	r.changed()
	return post, err
}

func (r *mailAuthorityContent) SearchPublished(ctx context.Context, query content.PublishedQuery) (content.PublishedPage, error) {
	page, err := r.Repository.SearchPublished(ctx, query)
	r.changed()
	return page, err
}

type mailAuthorityPreferences struct {
	Store
	failure error
}

func (s *mailAuthorityPreferences) Preferences(ctx context.Context, user int64) (Preferences, error) {
	if s.failure != nil {
		return Preferences{}, s.failure
	}
	return s.Store.Preferences(ctx, user)
}

type mailAuthorityUsers struct {
	Users
	failure error
}

func (u *mailAuthorityUsers) GetUser(ctx context.Context, id int64) (auth.User, error) {
	if u.failure != nil {
		return auth.User{}, u.failure
	}
	return u.Users.GetUser(ctx, id)
}

func TestMailRechecksRecipientAfterPreparation(t *testing.T) {
	for _, kind := range []string{Publication, Weekly} {
		for _, change := range []string{"unsubscribe", "kind-disabled", "resubscribe", "suspended", "deleted", "unverified", "new-verified-email", "preferences-outage", "users-outage", "allowed"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				s, posts, _, sender := notificationFixture(t)
				store := s.Store.(*FileStore)
				users := s.Users.(testUsers)
				now := time.Now().UTC()
				end := WeekEnd(now, "UTC")
				prefs := Defaults(2)
				prefs.Email, prefs.Weekly = true, true
				if _, err := store.SavePreferences(t.Context(), prefs, end.AddDate(0, 0, -14)); err != nil {
					t.Fatal(err)
				}
				if err := s.Engagement.Set(t.Context(), engagement.Input{UserID: 2, TargetID: 1, Kind: engagement.Follow, Active: true}); err != nil {
					t.Fatal(err)
				}
				post, err := posts.Create(t.Context(), content.PostInput{Title: "Delivery boundary", Slug: "delivery-boundary", AuthorID: 1, Status: content.StatusPublish, PublishedAt: end.Add(-time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				if kind == Weekly {
					err = store.QueueWeekly(t.Context(), 2, end, now)
				} else {
					events := content.ActivityEvents(content.Post{}, post)
					if len(events) != 1 {
						t.Fatalf("unexpected publication events: %v", events)
					}
					err = s.project(t.Context(), events[0], now)
				}
				if err != nil {
					t.Fatal(err)
				}
				job, ok, err := store.ClaimMail(t.Context(), now)
				if err != nil || !ok {
					t.Fatalf("claim failed: %v %v", ok, err)
				}
				preferences := &mailAuthorityPreferences{Store: store}
				accounts := &mailAuthorityUsers{Users: users}
				s.Store, s.Users = preferences, accounts
				failure := errors.New("injected recipient lookup outage")
				changed := false
				s.Content = &mailAuthorityContent{Repository: posts, afterRead: func() {
					changed = true
					p, err := store.Preferences(t.Context(), 2)
					if err != nil {
						t.Fatal(err)
					}
					u := users[2]
					switch change {
					case "unsubscribe", "resubscribe":
						if err := store.Unsubscribe(t.Context(), 2, p.UnsubscribeKey, now); err != nil {
							t.Fatal(err)
						}
						if change == "resubscribe" {
							p, err = store.Preferences(t.Context(), 2)
							if err != nil {
								t.Fatal(err)
							}
							p.Email, p.Weekly = true, true
							if _, err = store.SavePreferences(t.Context(), p, now.Add(time.Second)); err != nil {
								t.Fatal(err)
							}
						}
					case "kind-disabled":
						p.Publication, p.Weekly = false, false
						if _, err := store.SavePreferences(t.Context(), p, now); err != nil {
							t.Fatal(err)
						}
					case "suspended":
						u.Status = auth.UserStatusSuspended
						users[2] = u
					case "deleted":
						delete(users, 2)
					case "unverified":
						u.Email, u.EmailVerifiedAt = "new-unverified@example.test", nil
						users[2] = u
					case "new-verified-email":
						u.Email = "new-verified@example.test"
						users[2] = u
					case "preferences-outage":
						preferences.failure = failure
					case "users-outage":
						accounts.failure = failure
					}
				}}
				err = s.deliver(t.Context(), job, now)
				wantState, wantSent := "cancelled", 0
				switch change {
				case "allowed", "new-verified-email":
					wantState, wantSent = "sent", 1
				case "preferences-outage", "users-outage":
					wantState = "pending"
					if !errors.Is(err, failure) {
						t.Errorf("pre-send lookup failure lost: %v", err)
					}
				}
				if wantState != "pending" && err != nil {
					t.Error(err)
				}
				stored := store.state.Jobs[job.Key]
				if !changed || len(sender.Messages) != wantSent || stored.State != wantState || stored.LeaseUntil != nil || stored.Attempts != 1 {
					t.Fatalf("stale recipient reached SMTP: changed=%v sent=%d want=%d state=%s want=%s", changed, len(sender.Messages), wantSent, stored.State, wantState)
				}
				if wantSent == 1 && sender.Messages[0].To != users[2].Email {
					t.Fatalf("SMTP used a stale address: %q", sender.Messages[0].To)
				}
				if wantState == "pending" && !stored.AvailableAt.After(now) {
					t.Fatal("retry did not retain backoff")
				}
			})
		}
	}
}
