package virtualcurrency

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

type Users interface {
	GetUser(context.Context, int64) (auth.User, error)
}
type Service struct {
	Store   Store
	Catalog Catalog
	Users   Users
	Content content.Repository
}

func (s *Service) user(ctx context.Context, id int64) (auth.User, error) {
	user, err := s.Users.GetUser(ctx, id)
	if errors.Is(err, auth.ErrUserNotFound) {
		return user, ErrRewardIneligible
	}
	if err != nil {
		return user, err
	}
	if user.Status != auth.UserStatusActive {
		return user, ErrRewardIneligible
	}
	return user, nil
}

func (s *Service) CheckIn(ctx context.Context, id, version int64, requestID string) (RewardResult, error) {
	user, err := s.user(ctx, id)
	if err != nil {
		return RewardResult{}, err
	}
	return s.Store.Reward(ctx, RewardInput{UserID: id, Kind: RewardCheckIn, ExpectedVersion: version, RequestID: requestID, VerifiedEmail: user.EmailVerifiedAt != nil})
}

func (s *Service) Registration(ctx context.Context, id int64) (RewardResult, error) {
	user, err := s.user(ctx, id)
	if err != nil {
		return RewardResult{}, err
	}
	return s.Store.Reward(ctx, RewardInput{UserID: id, SourceID: id, Kind: RewardRegistration, OccurredAt: user.CreatedAt, VerifiedEmail: user.EmailVerifiedAt != nil})
}

// RegistrationAll applies each currency's own activation date, eligibility,
// budget and stable receipt. A failure in one currency does not skip the rest.
func (s *Service) RegistrationAll(ctx context.Context, id int64) error {
	return s.eachCurrency(ctx, func(service *Service) error {
		_, err := service.Registration(ctx, id)
		if RewardPolicyError(err) {
			return nil
		}
		return err
	})
}

func (s *Service) eachCurrency(ctx context.Context, fn func(*Service) error) error {
	if s.Catalog == nil {
		return fn(s)
	}
	items, err := s.Catalog.List(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range items {
		store, err := s.Catalog.Store(ctx, item.Key)
		if err == nil {
			err = fn(&Service{Store: store, Users: s.Users, Content: s.Content})
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("currency %s: %w", item.Key, err))
		}
	}
	return errors.Join(failures...)
}

// RewardPolicyError means no mutation was made because a configured business
// rule disallowed it. It must not undo an already committed registration/post.
func RewardPolicyError(err error) bool {
	return errors.Is(err, ErrDisabled) || errors.Is(err, ErrRewardIneligible) || errors.Is(err, ErrRewardLimit) || errors.Is(err, ErrBalanceLimit) || errors.Is(err, ErrIdempotencyConflict)
}

// Observe runs before the existing durable publication event is acknowledged.
// A crash between credit and acknowledgement safely replays the same post ID.
func (s *Service) Observe(ctx context.Context, event activity.Event) error {
	if event.Kind != activity.Publication {
		return nil
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if s.Catalog != nil {
		return s.eachCurrency(ctx, func(service *Service) error { return service.Observe(ctx, event) })
	}
	cfg, err := s.Store.Settings(ctx)
	if err != nil {
		return err
	}
	if !cfg.Enabled || !cfg.PublicationEnabled {
		return nil
	}
	post, err := s.Content.GetByID(ctx, event.PostID)
	if errors.Is(err, content.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	now := time.Now()
	if event.Due.After(now) || !content.PublicAt(post, now) || post.Type != content.TypePost || post.AuthorID != event.UserID || post.PublishedAt.After(event.Due) {
		return nil
	}
	user, err := s.user(ctx, post.AuthorID)
	if RewardPolicyError(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.Store.Reward(ctx, RewardInput{UserID: user.ID, SourceID: post.ID, Kind: RewardPublication, OccurredAt: post.CreatedAt, VerifiedEmail: user.EmailVerifiedAt != nil, ContentLength: visibleLength(post.ContentHTML)})
	if RewardPolicyError(err) {
		return nil
	}
	return err
}

// Count visible non-whitespace characters, not markup or source-format bytes.
func VisibleLength(body string) int { return visibleLength(body) }

func visibleLength(body string) int {
	tokens := html.NewTokenizer(strings.NewReader(body))
	count, skip := 0, false
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return count
		case html.StartTagToken:
			name, _ := tokens.TagName()
			if string(name) == "script" || string(name) == "style" {
				skip = true
			}
		case html.EndTagToken:
			name, _ := tokens.TagName()
			if string(name) == "script" || string(name) == "style" {
				skip = false
			}
		case html.TextToken:
			if !skip {
				for _, r := range string(tokens.Text()) {
					if !unicode.IsSpace(r) && !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) {
						count++
					}
				}
			}
		}
	}
}
