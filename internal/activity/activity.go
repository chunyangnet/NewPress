// Package activity defines durable, content-free source events. A source must
// commit events in the same transaction or file snapshot as the originating fact.
package activity

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	Publication       = "publication"
	ContentUpdated    = "content_updated"
	CommentPublic     = "comment_public"
	CommentModeration = "comment_moderation"
	PostModeration    = "post_moderation"
	Community         = "community"
)

type Event struct {
	Key        string    `json:"key"`
	Kind       string    `json:"kind"`
	ObjectID   int64     `json:"object_id"`
	PostID     int64     `json:"post_id"`
	UserID     int64     `json:"user_id"`
	ParentID   int64     `json:"parent_id,omitempty"`
	Version    int64     `json:"version"`
	Status     string    `json:"status"`
	Mentions   []string  `json:"mentions,omitempty"`
	At         time.Time `json:"at"`
	Due        time.Time `json:"due"`
	Circle     string    `json:"circle,omitempty"`
	TopicID    int64     `json:"topic_id,omitempty"`
	ReplyID    int64     `json:"reply_id,omitempty"`
	Recipients []int64   `json:"recipients,omitempty"`
}

type Source interface {
	PagedSource
	PendingActivity(context.Context, time.Time, int) ([]Event, error)
	AckActivity(context.Context, string) error
}

// PagedSource provides a stable key-order scan independent of acknowledgements.
// The cursor is only a scheduling hint: restarting from an empty cursor must
// include every unacknowledged event again, including previously failed events.
type PagedSource interface {
	PendingActivityAfter(context.Context, time.Time, int, string) ([]Event, error)
}

func New(kind string, object, post, user, version int64, status string, at time.Time) Event {
	return Event{Key: fmt.Sprintf("%s:%d:%d", kind, object, version), Kind: kind, ObjectID: object, PostID: post, UserID: user, Version: version, Status: status, At: at.UTC(), Due: at.UTC()}
}

func (e Event) Validate() error {
	if e.Kind == "questions" {
		if e.Key != fmt.Sprintf("questions:%d:%d", e.ObjectID, e.Version) || e.ObjectID <= 0 || e.Version <= 0 || e.UserID <= 0 || e.ReplyID < 0 || e.PostID != 0 || e.ParentID != 0 || e.TopicID != 0 || e.Circle != "" || len(e.Mentions) != 0 || len(e.Recipients) > 2 || e.At.IsZero() || e.Due.Before(e.At) {
			return errors.New("invalid question activity")
		}
		if e.Status != "answer" && e.Status != "note" && e.Status != "accept" && e.Status != "moderation" {
			return errors.New("invalid question status")
		}
		seen := map[int64]bool{}
		for _, id := range e.Recipients {
			if id <= 0 || seen[id] {
				return errors.New("invalid question recipient")
			}
			seen[id] = true
		}
		return nil
	}
	if e.Kind == Community {
		if !circleSlug.MatchString(e.Circle) || e.Key != fmt.Sprintf("community:%s:%d", e.Circle, e.Version) || e.Version <= 0 || e.UserID <= 0 || e.TopicID < 0 || e.ReplyID < 0 || e.At.IsZero() || e.Due.Before(e.At) || len(e.Recipients) > 2001 || len(e.Mentions) > 20 || e.ObjectID != 0 || e.PostID != 0 || e.ParentID != 0 || !communityActions[e.Status] {
			return errors.New("invalid community activity")
		}
		seen := map[int64]bool{}
		for _, id := range e.Recipients {
			if id <= 0 || seen[id] {
				return errors.New("invalid community recipient")
			}
			seen[id] = true
		}
		names := map[string]bool{}
		for _, name := range e.Mentions {
			if !username.MatchString(name) || names[name] {
				return errors.New("invalid community mention")
			}
			names[name] = true
		}
		return nil
	}
	if e.Circle != "" || e.TopicID != 0 || e.ReplyID != 0 || len(e.Recipients) != 0 {
		return errors.New("invalid activity scope")
	}
	if e.Kind != Publication && e.Kind != CommentPublic && e.Kind != CommentModeration && e.Kind != PostModeration && e.Kind != ContentUpdated ||
		e.Key != fmt.Sprintf("%s:%d:%d", e.Kind, e.ObjectID, e.Version) || e.ObjectID <= 0 || e.PostID <= 0 || e.UserID < 0 || e.ParentID < 0 || e.Version <= 0 || e.At.IsZero() || e.Due.Before(e.At) || len(e.Mentions) > 20 {
		return errors.New("invalid activity event")
	}
	validStatus := false
	switch e.Kind {
	case Publication:
		validStatus = e.Status == "publish" || e.Status == "future"
	case CommentPublic:
		validStatus = e.Status == "approved"
	case CommentModeration:
		validStatus = e.Status == "approved" || e.Status == "pending" || e.Status == "spam" || e.Status == "trash"
	case ContentUpdated:
		validStatus = e.Status == "draft" || e.Status == "pending" || e.Status == "publish" || e.Status == "future" || e.Status == "private" || e.Status == "trash"
	case PostModeration:
		validStatus = e.Status == "draft" || e.Status == "publish" || e.Status == "future" || e.Status == "private" || e.Status == "trash"
	}
	if !validStatus || e.Kind != CommentPublic && (len(e.Mentions) > 0 || e.ParentID != 0 || e.UserID == 0) || e.Kind == CommentPublic && e.ParentID == e.ObjectID || (e.Kind == Publication || e.Kind == PostModeration || e.Kind == ContentUpdated) && e.ObjectID != e.PostID {
		return errors.New("invalid activity scope")
	}
	seen := map[string]bool{}
	for _, name := range e.Mentions {
		if !username.MatchString(name) || seen[name] {
			return errors.New("invalid activity mention")
		}
		seen[name] = true
	}
	return nil
}

func Validate(events []Event) error {
	seen := map[string]bool{}
	for _, e := range events {
		if err := e.Validate(); err != nil {
			return err
		}
		if seen[e.Key] {
			return errors.New("duplicate activity event")
		}
		seen[e.Key] = true
	}
	return nil
}

func Pending(events []Event, now time.Time, limit int) []Event {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	items := []Event{}
	for _, e := range events {
		if !e.Due.After(now) {
			e.Mentions = append([]string(nil), e.Mentions...)
			e.Recipients = append([]int64(nil), e.Recipients...)
			items = append(items, e)
			if len(items) == limit {
				break
			}
		}
	}
	return items
}

func Without(events []Event, key string) []Event {
	items := make([]Event, 0, len(events))
	for _, e := range events {
		if e.Key != key {
			items = append(items, e)
		}
	}
	return items
}

var username = regexp.MustCompile(`^[\p{L}\p{N}_.-]{3,80}$`)
var circleSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var communityActions = map[string]bool{"topic": true, "reply": true, "join": true, "member": true, "remove_member": true, "transfer": true, "accept": true, "hide_topic": true, "hide_reply": true, "report": true, "review_report": true}
var mention = regexp.MustCompile(`(?:^|[\s(（,，。!！?？:：;；、\[【{「『“‘"'])@([\p{L}\p{N}_.-]{3,80})(?:$|[\s,，。!！?？:：;；、)）\]】}」』”’"'])`)

// Mentions are explicit @usernames in account comments, never email addresses.
func Mentions(text string) []string {
	result := []string{}
	seen := map[string]bool{}
	// A lookahead is unavailable in RE2. Keep the trailing delimiter for the next
	// match so adjacent mentions separated by a single space are both found.
	for len(text) > 0 && len(result) < 20 {
		match := mention.FindStringSubmatchIndex(text)
		if match == nil {
			break
		}
		name := strings.ToLower(text[match[2]:match[3]])
		if !seen[name] {
			result = append(result, name)
			seen[name] = true
		}
		text = text[match[3]:]
	}
	return result
}
