// Package notification owns account notification preferences, inboxes and the
// durable delivery queue. No titles, bodies, or email addresses are queued.
package notification

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"newpress.local/newpress/internal/activity"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid  = errors.New("invalid notification input")
	ErrConflict = errors.New("notification version conflict")
)

const PageSize = 50
const (
	Reply       = "reply"
	Mention     = "mention"
	Publication = "publication"
	Moderation  = "moderation"
	Weekly      = "weekly"
)

type Preferences struct {
	UserID         int64     `json:"user_id"`
	Version        int64     `json:"version"`
	Reply          bool      `json:"reply"`
	Mention        bool      `json:"mention"`
	Publication    bool      `json:"publication"`
	Moderation     bool      `json:"moderation"`
	Email          bool      `json:"email"`
	Weekly         bool      `json:"weekly"`
	EmailSince     time.Time `json:"email_since"`
	WeeklySince    time.Time `json:"weekly_since"`
	UnsubscribeKey string    `json:"unsubscribe_key"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func Defaults(user int64) Preferences {
	return Preferences{UserID: user, Reply: true, Mention: true, Publication: true, Moderation: true}
}
func (p Preferences) Allows(kind string) bool {
	switch kind {
	case Reply:
		return p.Reply
	case Mention:
		return p.Mention
	case Publication:
		return p.Publication
	case Moderation:
		return p.Moderation
	case Weekly:
		return p.Weekly
	}
	return false
}

// UpdatedPreferences enforces compare-and-swap and rotates the unsubscribe key
// when email is enabled again. It ignores all client-supplied internal fields.
func UpdatedPreferences(old, input Preferences, now time.Time) (Preferences, error) {
	if old.UserID <= 0 || input.UserID != old.UserID {
		return Preferences{}, ErrInvalid
	}
	if input.Version != old.Version {
		return Preferences{}, ErrConflict
	}
	p := old
	p.Version++
	p.UpdatedAt = now.UTC()
	p.Reply, p.Mention, p.Publication, p.Moderation = input.Reply, input.Mention, input.Publication, input.Moderation
	p.Email, p.Weekly = input.Email, input.Weekly
	if input.Email && !old.Email {
		p.EmailSince = now.UTC()
	}
	if input.Weekly && !old.Weekly {
		p.WeeklySince = now.UTC()
	}
	if p.UnsubscribeKey == "" || (input.Email || input.Weekly) && !old.Email && !old.Weekly {
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return Preferences{}, err
		}
		p.UnsubscribeKey = hex.EncodeToString(secret[:])
	}
	return p, nil
}

type Delivery struct {
	InboxOnly bool           `json:"inbox_only,omitempty"`
	UserID    int64          `json:"user_id"`
	Kind      string         `json:"kind"`
	Event     activity.Event `json:"event"`
}
type Notice struct {
	ID int64 `json:"id"`
	Delivery
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
	ClearedAt *time.Time `json:"cleared_at,omitempty"`
}
type Page struct {
	Items   []Notice
	Unread  int
	Through int64
	HasMore bool
}
type Query struct {
	Before     int64
	UnreadOnly bool
	Kind       string
}
type Job struct {
	Key string `json:"key"`
	Delivery
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	AvailableAt time.Time  `json:"available_at"`
	LeaseUntil  *time.Time `json:"lease_until,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}
type MailStatus struct {
	Pending   int `json:"pending"`
	Failed    int `json:"failed"`
	Uncertain int `json:"uncertain"`
}

type Store interface {
	Ready(context.Context) error
	Preferences(context.Context, int64) (Preferences, error)
	SavePreferences(context.Context, Preferences, time.Time) (Preferences, error)
	Unsubscribe(context.Context, int64, string, time.Time) error
	// Project atomically records the receipt, inbox entries and optional mail.
	// Receipts survive clearing an inbox and acknowledge a whole source event.
	Project(context.Context, string, []Delivery, time.Time) error
	Notices(context.Context, int64, Query) (Page, error)
	UpdateNotices(context.Context, int64, int64, string, time.Time) error
	Subscribers(context.Context, int64, int) ([]Preferences, error)
	QueueWeekly(context.Context, int64, time.Time, time.Time) error
	ClaimMail(context.Context, time.Time) (Job, bool, error)
	// FinishMail requires the current, unexpired claim returned by ClaimMail.
	// A stale or altered claim returns ErrConflict without changing the queue.
	FinishMail(context.Context, Job, string, time.Time) error
	MailStatus(context.Context, int64) (MailStatus, error)
	RetryFailed(context.Context, int64, time.Time) error
}

func validReceipt(receipt string) bool {
	return receipt != "" && len(receipt) <= 160 && utf8.ValidString(receipt) && !strings.ContainsRune(receipt, '\x00')
}

// ValidateProjection preserves receipt identities across JSON and PostgreSQL
// storage, including projections that have no matching recipients.
func ValidateProjection(receipt string, deliveries []Delivery, now time.Time) error {
	if !validReceipt(receipt) || now.IsZero() {
		return ErrInvalid
	}
	for _, delivery := range deliveries {
		if err := ValidateDelivery(delivery); err != nil {
			return err
		}
	}
	return nil
}

func ValidateDelivery(d Delivery) error {
	if d.UserID <= 0 || d.Kind != Reply && d.Kind != Mention && d.Kind != Publication && d.Kind != Moderation {
		return ErrInvalid
	}
	if d.Event.Kind == "questions" {
		if d.Kind != Reply && d.Kind != Moderation {
			return ErrInvalid
		}
		return d.Event.Validate()
	}
	if d.Event.Kind == activity.Community {
		return d.Event.Validate()
	}
	if (d.Kind == Reply || d.Kind == Mention) && d.Event.Kind != activity.CommentPublic || d.Kind == Publication && d.Event.Kind != activity.Publication || d.Kind == Moderation && d.Event.Kind != activity.CommentModeration && d.Event.Kind != activity.PostModeration {
		return ErrInvalid
	}
	return d.Event.Validate()
}
func JobKey(receipt string, d Delivery) string {
	key := fmt.Sprintf("%s:%s:%d", receipt, d.Kind, d.UserID)
	if len(key) <= 160 {
		return key
	}
	// Preserve existing task identities while fitting the database key limit.
	// This format cannot collide with legacy keys, which end in :kind:user.
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(key)))
}
func WeeklyKey(user int64, end time.Time) string {
	return fmt.Sprintf("weekly:%d:%s", user, end.UTC().Format("20060102T1504"))
}
func WantsEmail(p Preferences, d Delivery) bool {
	return !d.InboxOnly && p.Email && p.Allows(d.Kind) && !d.Event.Due.Before(p.EmailSince)
}
func NewJob(key string, d Delivery, now time.Time) Job {
	return Job{Key: key, Delivery: d, State: "pending", AvailableAt: now, CreatedAt: now, UpdatedAt: now}
}
func validMailClaim(job Job, now time.Time) bool {
	return job.State == "sending" && job.Attempts > 0 && !job.UpdatedAt.IsZero() && job.LeaseUntil != nil && !now.Before(job.UpdatedAt) && now.Before(*job.LeaseUntil)
}

func Finished(job Job, state string, now time.Time) (Job, error) {
	if state != "sent" && state != "retry" && state != "uncertain" && state != "cancelled" {
		return Job{}, ErrInvalid
	}
	if !validMailClaim(job, now) {
		return Job{}, ErrConflict
	}
	job.State, job.UpdatedAt, job.LeaseUntil = state, now, nil
	if state == "retry" {
		job.State = "pending"
		if job.Attempts >= 8 {
			job.State = "failed"
		} else {
			job.AvailableAt = now.Add(time.Minute * time.Duration(1<<min(job.Attempts, 10)))
		}
	}
	return job, nil
}
