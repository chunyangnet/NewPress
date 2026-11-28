// Package community implements the opt-in, trusted builtin community extension.
// Domain authorization and mutation rules are shared by all persistence adapters.
package community

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/activity"
)

const (
	MaxCircles       = 100
	MaxMembers       = 2000
	MaxTopics        = 500
	MaxReplies       = 5000
	MaxCircleBytes   = 4 << 20
	MaxDocumentBytes = 32 << 20
)

var (
	ErrInvalid   = errors.New("community: invalid input")
	ErrForbidden = errors.New("community: forbidden")
	ErrNotFound  = errors.New("community: not found")
	ErrConflict  = errors.New("community: version or request conflict")
	ErrCapacity  = errors.New("community: capacity reached")
	ErrChanged   = errors.New("community: file changed externally")
	slugPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	keyPattern   = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,80}$`)
)

type Actor struct {
	ID    int64
	Admin bool
}
type Member struct {
	UserID     int64     `json:"user_id"`
	Role       string    `json:"role"`
	Reason     string    `json:"reason,omitempty"`
	JoinedAt   time.Time `json:"joined_at,omitempty"`
	ReviewedAt time.Time `json:"reviewed_at,omitempty"`
}
type Reply struct {
	ID          int64     `json:"id"`
	AuthorID    int64     `json:"author_id"`
	Body        string    `json:"body"`
	Hidden      bool      `json:"hidden"`
	CreatedAt   time.Time `json:"created_at"`
	EditedAt    time.Time `json:"edited_at,omitempty"`
	Format      string    `json:"format,omitempty"`
	Attachments []string  `json:"attachments,omitempty"`
}
type Topic struct {
	ID          int64     `json:"id"`
	AuthorID    int64     `json:"author_id"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Kind        string    `json:"kind"`
	Pinned      bool      `json:"pinned"`
	Featured    bool      `json:"featured"`
	Locked      bool      `json:"locked"`
	Hidden      bool      `json:"hidden"`
	AcceptedID  int64     `json:"accepted_id"`
	CreatedAt   time.Time `json:"created_at"`
	EditedAt    time.Time `json:"edited_at,omitempty"`
	Replies     []Reply   `json:"replies"`
	Format      string    `json:"format,omitempty"`
	Attachments []string  `json:"attachments,omitempty"`
	Archived    bool      `json:"archived,omitempty"`
}
type Receipt struct {
	Key     string `json:"key"`
	ActorID int64  `json:"actor_id"`
	Digest  string `json:"digest"`
	Result  Result `json:"result"`
}
type Circle struct {
	Slug        string           `json:"slug"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Rules       string           `json:"rules"`
	JoinPolicy  string           `json:"join_policy"`
	Visibility  string           `json:"visibility"`
	PostPolicy  string           `json:"post_policy,omitempty"`
	JoinReason  bool             `json:"join_reason,omitempty"`
	OwnerID     int64            `json:"owner_id"`
	Version     int64            `json:"version"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	Members     []Member         `json:"members"`
	Topics      []Topic          `json:"topics"`
	Receipts    []Receipt        `json:"receipts"`
	NextTopic   int64            `json:"next_topic"`
	NextReply   int64            `json:"next_reply"`
	Archived    bool             `json:"archived,omitempty"`
	Attachments []Attachment     `json:"attachments,omitempty"`
	Reports     []Report         `json:"reports,omitempty"`
	NextReport  int64            `json:"next_report,omitempty"`
	Changes     []Change         `json:"changes,omitempty"`
	Activities  []activity.Event `json:"activities,omitempty"`
}
type Command struct {
	Action       string      `json:"action"`
	Key          string      `json:"key"`
	Version      int64       `json:"version"`
	Name         string      `json:"name,omitempty"`
	Description  string      `json:"description,omitempty"`
	Rules        string      `json:"rules,omitempty"`
	JoinPolicy   string      `json:"join_policy,omitempty"`
	Visibility   string      `json:"visibility,omitempty"`
	PostPolicy   string      `json:"post_policy,omitempty"`
	JoinReason   bool        `json:"join_reason,omitempty"`
	Reason       string      `json:"reason,omitempty"`
	OwnerID      int64       `json:"owner_id,omitempty"`
	UserID       int64       `json:"user_id,omitempty"`
	Role         string      `json:"role,omitempty"`
	TopicID      int64       `json:"topic_id,omitempty"`
	ReplyID      int64       `json:"reply_id,omitempty"`
	Title        string      `json:"title,omitempty"`
	Body         string      `json:"body,omitempty"`
	Kind         string      `json:"kind,omitempty"`
	Enabled      *bool       `json:"enabled,omitempty"`
	Format       string      `json:"format,omitempty"`
	Attachments  []string    `json:"attachments,omitempty"`
	Attachment   *Attachment `json:"attachment,omitempty"`
	AttachmentID string      `json:"attachment_id,omitempty"`
	ReportID     int64       `json:"report_id,omitempty"`
	Category     string      `json:"category,omitempty"`
	Status       string      `json:"status,omitempty"`
	Note         string      `json:"note,omitempty"`
}
type Result struct {
	Slug         string `json:"slug"`
	Version      int64  `json:"version"`
	TopicID      int64  `json:"topic_id,omitempty"`
	ReplyID      int64  `json:"reply_id,omitempty"`
	ReplyPage    int    `json:"reply_page,omitempty"`
	Replay       bool   `json:"replay,omitempty"`
	AttachmentID string `json:"attachment_id,omitempty"`
	ReportID     int64  `json:"report_id,omitempty"`
}
type Store interface {
	Ready(context.Context) error
	List(context.Context) ([]Circle, error) // Header and membership only; no topic body or receipts.
	Get(context.Context, string) (Circle, error)
	Apply(context.Context, string, Actor, Command, time.Time) (Result, error)
	activity.Source
}

func ValidSlug(s string) bool { return slugPattern.MatchString(s) }
func Role(c Circle, a Actor) string {
	if a.ID <= 0 {
		return ""
	}
	if a.ID == c.OwnerID {
		return "owner"
	}
	for _, m := range c.Members {
		if m.UserID == a.ID {
			return m.Role
		}
	}
	return ""
}
func Manages(c Circle, a Actor) bool {
	r := Role(c, a)
	return a.ID > 0 && (a.Admin || r == "owner" || r == "moderator")
}
func Owns(c Circle, a Actor) bool { return a.ID > 0 && (a.Admin || a.ID == c.OwnerID) }
func CanRead(c Circle, a Actor) bool {
	r := Role(c, a)
	return c.Visibility == "public" || Manages(c, a) || r == "member"
}
func CanPost(c Circle, a Actor) bool {
	r := Role(c, a)
	return !c.Archived && (Manages(c, a) || r == "member" && c.PostPolicy != "moderators")
}
func CanEditTopic(c Circle, t Topic, a Actor) bool {
	return !c.Archived && !t.Archived && !t.Hidden && !t.Locked && (Manages(c, a) || CanPost(c, a) && a.ID == t.AuthorID)
}
func CanEditReply(c Circle, t Topic, r Reply, a Actor) bool {
	return !c.Archived && !t.Archived && !r.Hidden && !t.Hidden && !t.Locked && (Manages(c, a) || CanPost(c, a) && a.ID == r.AuthorID)
}
func Clone(c Circle) Circle {
	// Snapshots cross storage and transaction boundaries. Copy all mutable
	// collections without encoding every topic and reply while holding a lock.
	out := c
	out.Members = slices.Clone(c.Members)
	out.Receipts = slices.Clone(c.Receipts)
	out.Attachments = slices.Clone(c.Attachments)
	out.Reports = slices.Clone(c.Reports)
	out.Changes = slices.Clone(c.Changes)
	out.Topics = slices.Clone(c.Topics)
	for i := range out.Topics {
		topic := &out.Topics[i]
		topic.Attachments = slices.Clone(topic.Attachments)
		topic.Replies = slices.Clone(topic.Replies)
		for j := range topic.Replies {
			topic.Replies[j].Attachments = slices.Clone(topic.Replies[j].Attachments)
		}
	}
	out.Activities = slices.Clone(c.Activities)
	for i := range out.Activities {
		out.Activities[i].Mentions = slices.Clone(out.Activities[i].Mentions)
		out.Activities[i].Recipients = slices.Clone(out.Activities[i].Recipients)
	}
	return out
}
func Header(c Circle) Circle {
	c.Topics = nil
	c.Receipts = nil
	c.Reports, c.Changes, c.Activities, c.Attachments = nil, nil, nil, nil
	c.Members = slices.Clone(c.Members)
	return c
}
func textValid(s string, min, max int, multiline bool) bool {
	if !utf8.ValidString(s) || strings.TrimSpace(s) != s || utf8.RuneCountInString(s) < min || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\t')) {
			return false
		}
	}
	return true
}
func attributes(c Circle) bool {
	return ValidSlug(c.Slug) && textValid(c.Name, 1, 80, false) && textValid(c.Description, 0, 500, true) && textValid(c.Rules, 0, 3000, true) && (c.JoinPolicy == "open" || c.JoinPolicy == "approval" || c.JoinPolicy == "closed") && (c.Visibility == "public" || c.Visibility == "members") && (c.PostPolicy == "" || c.PostPolicy == "members" || c.PostPolicy == "moderators") && c.OwnerID > 0
}
func commandValid(cmd Command) bool {
	if !keyPattern.MatchString(cmd.Key) || cmd.Version < 0 {
		return false
	}
	fields := map[string]string{
		"create": "name description rules join_policy visibility owner_id post_policy join_reason", "settings": "name description rules join_policy visibility post_policy join_reason",
		"join": "reason", "leave": "", "member": "user_id role", "transfer": "user_id", "remove_member": "user_id",
		"topic": "title body kind format attachments", "reply": "topic_id body format attachments", "accept": "topic_id reply_id",
		"edit_topic": "topic_id title body format attachments", "edit_reply": "topic_id reply_id body format attachments",
		"pin": "topic_id enabled", "feature": "topic_id enabled", "lock": "topic_id enabled", "hide_topic": "topic_id enabled", "hide_reply": "topic_id reply_id enabled",
		"archive_circle": "enabled", "archive_topic": "topic_id enabled", "delete_topic": "topic_id", "delete_reply": "topic_id reply_id",
		"report": "topic_id reply_id category reason", "review_report": "report_id status note enabled", "clear_reports": "",
		"add_attachment": "attachment", "remove_attachment": "attachment_id",
	}
	allowed, ok := fields[cmd.Action]
	if !ok {
		return false
	}
	b, _ := json.Marshal(cmd)
	var values map[string]any
	_ = json.Unmarshal(b, &values)
	for field := range values {
		if field != "action" && field != "key" && field != "version" && !slices.Contains(strings.Fields(allowed), field) {
			return false
		}
	}
	return true
}
func commandDigest(cmd Command) string {
	b, _ := json.Marshal(cmd)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Mutate receives a private candidate. Authorization and the expected version
// are checked under the persistence adapter's lock before changing any facts.
func Mutate(current *Circle, slug string, a Actor, cmd Command, now time.Time) (Circle, Result, error) {
	fail := func(e error) (Circle, Result, error) { return Circle{}, Result{}, e }
	if !ValidSlug(slug) || a.ID <= 0 || !commandValid(cmd) || now.IsZero() {
		return fail(ErrInvalid)
	}
	digest := commandDigest(cmd)
	var c Circle
	if current == nil {
		if cmd.Action != "create" {
			return fail(ErrNotFound)
		}
		if !a.Admin {
			return fail(ErrForbidden)
		}
		if cmd.Version != 0 {
			return fail(ErrConflict)
		}
		c = Circle{Slug: slug, Name: cmd.Name, Description: cmd.Description, Rules: cmd.Rules, JoinPolicy: cmd.JoinPolicy, Visibility: cmd.Visibility, PostPolicy: cmd.PostPolicy, JoinReason: cmd.JoinReason, OwnerID: cmd.OwnerID, CreatedAt: now.UTC(), NextTopic: 1, NextReply: 1, Members: []Member{}, Topics: []Topic{}, Receipts: []Receipt{}}
		if !attributes(c) {
			return fail(ErrInvalid)
		}
	} else {
		c = Clone(*current)
		for _, r := range c.Receipts {
			if r.Key == cmd.Key && r.ActorID == a.ID {
				if r.Digest != digest {
					return fail(ErrConflict)
				}
				result := r.Result
				result.Replay = true
				return c, result, nil
			}
		}
		if cmd.Action == "create" || cmd.Version != c.Version {
			return fail(ErrConflict)
		}
	}
	result := Result{Slug: slug}
	manage, owner := Manages(c, a), Owns(c, a)
	memberIndex := func(id int64) int { return slices.IndexFunc(c.Members, func(m Member) bool { return m.UserID == id }) }
	setMember := func(id int64, role string) error {
		i := memberIndex(id)
		if i < 0 {
			if len(c.Members) >= MaxMembers {
				return ErrCapacity
			}
			c.Members = append(c.Members, Member{UserID: id, Role: role, JoinedAt: now.UTC(), Reason: cmd.Reason})
		} else {
			c.Members[i].Role = role
		}
		return nil
	}
	topicIndex := func(id int64) int { return slices.IndexFunc(c.Topics, func(t Topic) bool { return t.ID == id }) }
	switch cmd.Action {
	case "create":
	case "settings":
		if !owner {
			return fail(ErrForbidden)
		}
		c.Name = cmd.Name
		c.Description = cmd.Description
		c.Rules = cmd.Rules
		c.JoinPolicy = cmd.JoinPolicy
		c.Visibility = cmd.Visibility
		c.PostPolicy = cmd.PostPolicy
		c.JoinReason = cmd.JoinReason
		if !attributes(c) {
			return fail(ErrInvalid)
		}
	case "join":
		if !textValid(cmd.Reason, 0, 500, true) || c.JoinReason && c.JoinPolicy == "approval" && cmd.Reason == "" {
			return fail(ErrInvalid)
		}
		role := Role(c, a)
		if role == "banned" {
			return fail(ErrForbidden)
		}
		if role == "" {
			if c.JoinPolicy == "closed" || c.Archived {
				return fail(ErrForbidden)
			}
			role = "member"
			if c.JoinPolicy == "approval" {
				role = "pending"
			}
			if err := setMember(a.ID, role); err != nil {
				return fail(err)
			}
		}
	case "leave":
		if a.ID == c.OwnerID || Role(c, a) == "banned" {
			return fail(ErrForbidden)
		}
		if i := memberIndex(a.ID); i >= 0 {
			c.Members = slices.Delete(c.Members, i, i+1)
		}
	case "member":
		if !manage || cmd.UserID <= 0 || cmd.UserID == c.OwnerID || cmd.UserID == a.ID {
			return fail(ErrForbidden)
		}
		i := memberIndex(cmd.UserID)
		if i < 0 {
			return fail(ErrNotFound)
		}
		if cmd.Role != "member" && cmd.Role != "moderator" && cmd.Role != "banned" {
			return fail(ErrInvalid)
		}
		if !owner && (cmd.Role == "moderator" || c.Members[i].Role == "moderator") {
			return fail(ErrForbidden)
		}
		c.Members[i].Role = cmd.Role
		c.Members[i].ReviewedAt = now.UTC()
	case "remove_member":
		if !manage || cmd.UserID <= 0 || cmd.UserID == c.OwnerID || cmd.UserID == a.ID {
			return fail(ErrForbidden)
		}
		i := memberIndex(cmd.UserID)
		if i < 0 {
			return fail(ErrNotFound)
		}
		if !owner && c.Members[i].Role == "moderator" {
			return fail(ErrForbidden)
		}
		c.Members = slices.Delete(c.Members, i, i+1)
	case "transfer":
		if !owner {
			return fail(ErrForbidden)
		}
		i := memberIndex(cmd.UserID)
		if i < 0 || (c.Members[i].Role != "member" && c.Members[i].Role != "moderator") {
			return fail(ErrInvalid)
		}
		oldOwner := c.OwnerID
		c.OwnerID = cmd.UserID
		c.Members[i] = Member{UserID: oldOwner, Role: "moderator", JoinedAt: c.CreatedAt, ReviewedAt: now.UTC()}
	case "topic":
		if !CanPost(c, a) {
			return fail(ErrForbidden)
		}
		if !textValid(cmd.Title, 1, 160, false) || !textValid(cmd.Body, 1, 10000, true) || (cmd.Kind != "discussion" && cmd.Kind != "question") {
			return fail(ErrInvalid)
		}
		if len(c.Topics) >= MaxTopics {
			return fail(ErrCapacity)
		}
		result.TopicID = c.NextTopic
		c.NextTopic++
		c.Topics = append(c.Topics, Topic{ID: result.TopicID, AuthorID: a.ID, Title: cmd.Title, Body: cmd.Body, Kind: cmd.Kind, Format: cmd.Format, Attachments: slices.Clone(cmd.Attachments), CreatedAt: now.UTC(), Replies: []Reply{}})
		if err := bindAttachments(&c, a, cmd.Attachments, result.TopicID, 0); err != nil {
			return fail(err)
		}
	case "archive_circle", "report", "review_report", "clear_reports", "add_attachment", "remove_attachment":
		if err := mutateFeature(&c, a, cmd, &result, now); err != nil {
			return fail(err)
		}
	default:
		if !CanRead(c, a) {
			return fail(ErrForbidden)
		}
		i := topicIndex(cmd.TopicID)
		if i < 0 {
			return fail(ErrNotFound)
		}
		t := &c.Topics[i]
		if t.Hidden && !manage {
			return fail(ErrNotFound)
		}
		result.TopicID = t.ID
		switch cmd.Action {
		case "edit_topic":
			if !CanEditTopic(c, *t, a) {
				return fail(ErrForbidden)
			}
			if !textValid(cmd.Title, 1, 160, false) || !textValid(cmd.Body, 1, 10000, true) {
				return fail(ErrInvalid)
			}
			t.Title, t.Body, t.EditedAt = cmd.Title, cmd.Body, now.UTC()
			t.Format, t.Attachments = cmd.Format, slices.Clone(cmd.Attachments)
			if err := bindAttachments(&c, a, cmd.Attachments, t.ID, 0); err != nil {
				return fail(err)
			}
		case "edit_reply":
			ri := slices.IndexFunc(t.Replies, func(r Reply) bool { return r.ID == cmd.ReplyID && (!r.Hidden || manage) })
			if ri < 0 {
				return fail(ErrNotFound)
			}
			if !CanEditReply(c, *t, t.Replies[ri], a) {
				return fail(ErrForbidden)
			}
			if !textValid(cmd.Body, 1, 6000, true) {
				return fail(ErrInvalid)
			}
			t.Replies[ri].Body, t.Replies[ri].EditedAt = cmd.Body, now.UTC()
			t.Replies[ri].Format, t.Replies[ri].Attachments = cmd.Format, slices.Clone(cmd.Attachments)
			if err := bindAttachments(&c, a, cmd.Attachments, t.ID, cmd.ReplyID); err != nil {
				return fail(err)
			}
			// Editing an accepted answer requires the question author to accept it again.
			if t.AcceptedID == cmd.ReplyID {
				t.AcceptedID = 0
			}
			result.ReplyID = cmd.ReplyID
			visible := 0
			for _, reply := range t.Replies[:ri+1] {
				if !reply.Hidden || manage {
					visible++
				}
			}
			result.ReplyPage = (visible + 24) / 25
		case "reply":
			if !CanPost(c, a) || t.Locked || t.Hidden || t.Archived {
				return fail(ErrForbidden)
			}
			if !textValid(cmd.Body, 1, 6000, true) {
				return fail(ErrInvalid)
			}
			count := 0
			for _, topic := range c.Topics {
				count += len(topic.Replies)
			}
			if count >= MaxReplies || len(t.Replies) >= 250 {
				return fail(ErrCapacity)
			}
			result.ReplyID = c.NextReply
			c.NextReply++
			t.Replies = append(t.Replies, Reply{ID: result.ReplyID, AuthorID: a.ID, Body: cmd.Body, Format: cmd.Format, Attachments: slices.Clone(cmd.Attachments), CreatedAt: now.UTC()})
			if err := bindAttachments(&c, a, cmd.Attachments, t.ID, result.ReplyID); err != nil {
				return fail(err)
			}
			visible := 0
			for _, reply := range t.Replies {
				if !reply.Hidden || manage {
					visible++
				}
			}
			result.ReplyPage = (visible + 24) / 25
		case "accept":
			if !CanPost(c, a) || (!manage && a.ID != t.AuthorID) {
				return fail(ErrForbidden)
			}
			if t.Kind != "question" || cmd.ReplyID < 0 || t.Hidden {
				return fail(ErrInvalid)
			}
			if cmd.ReplyID > 0 {
				ri := slices.IndexFunc(t.Replies, func(r Reply) bool { return r.ID == cmd.ReplyID && !r.Hidden })
				if ri < 0 {
					return fail(ErrNotFound)
				}
			}
			t.AcceptedID = cmd.ReplyID
		case "archive_topic", "delete_topic", "delete_reply":
			if err := mutateTopicLifecycle(&c, a, cmd, &result); err != nil {
				return fail(err)
			}
		case "pin", "feature", "lock", "hide_topic", "hide_reply":
			if !manage {
				return fail(ErrForbidden)
			}
			if cmd.Enabled == nil {
				return fail(ErrInvalid)
			}
			switch cmd.Action {
			case "pin":
				t.Pinned = *cmd.Enabled
			case "feature":
				t.Featured = *cmd.Enabled
			case "lock":
				t.Locked = *cmd.Enabled
			case "hide_topic":
				t.Hidden = *cmd.Enabled
			case "hide_reply":
				ri := slices.IndexFunc(t.Replies, func(r Reply) bool { return r.ID == cmd.ReplyID })
				if ri < 0 {
					return fail(ErrNotFound)
				}
				t.Replies[ri].Hidden = *cmd.Enabled
				if *cmd.Enabled && t.AcceptedID == cmd.ReplyID {
					t.AcceptedID = 0
				}
			}
		default:
			return fail(ErrInvalid)
		}
	}
	c.Version++
	c.UpdatedAt = now.UTC()
	result.Version = c.Version
	if err := recordChange(&c, a, cmd, result, now); err != nil {
		return fail(err)
	}
	c.Receipts = append(c.Receipts, Receipt{cmd.Key, a.ID, digest, result})
	if len(c.Receipts) > 512 {
		c.Receipts = slices.Clone(c.Receipts[len(c.Receipts)-512:])
	}
	if err := Validate(c); err != nil {
		return fail(err)
	}
	b, _ := json.Marshal(c)
	if len(b) > MaxCircleBytes {
		return fail(ErrCapacity)
	}
	return c, result, nil
}

func Validate(c Circle) error {
	bad := func() error { return fmt.Errorf("%w: stored circle", ErrInvalid) }
	if err := validateFeatures(c); err != nil {
		return err
	}
	if !attributes(c) || c.Version < 1 || c.NextTopic < 1 || c.NextReply < 1 || c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) || len(c.Members) > MaxMembers || len(c.Topics) > MaxTopics || len(c.Receipts) > 512 {
		return bad()
	}
	users := map[int64]bool{c.OwnerID: true}
	for _, m := range c.Members {
		if m.UserID <= 0 || users[m.UserID] || !slices.Contains([]string{"member", "moderator", "pending", "banned"}, m.Role) || !textValid(m.Reason, 0, 500, true) || !m.ReviewedAt.IsZero() && !m.JoinedAt.IsZero() && m.ReviewedAt.Before(m.JoinedAt) {
			return bad()
		}
		users[m.UserID] = true
	}
	topics, replies := map[int64]bool{}, map[int64]bool{}
	for _, t := range c.Topics {
		if t.ID <= 0 || t.ID >= c.NextTopic || topics[t.ID] || t.AuthorID <= 0 || t.CreatedAt.IsZero() || !t.EditedAt.IsZero() && t.EditedAt.Before(t.CreatedAt) || !textValid(t.Title, 1, 160, false) || !textValid(t.Body, 1, 10000, true) || (t.Kind != "question" && t.Kind != "discussion") || len(t.Replies) > 250 {
			return bad()
		}
		topics[t.ID] = true
		accepted := t.AcceptedID == 0
		for _, r := range t.Replies {
			if r.ID <= 0 || r.ID >= c.NextReply || replies[r.ID] || r.AuthorID <= 0 || r.CreatedAt.IsZero() || !r.EditedAt.IsZero() && r.EditedAt.Before(r.CreatedAt) || !textValid(r.Body, 1, 6000, true) {
				return bad()
			}
			replies[r.ID] = true
			if r.ID == t.AcceptedID && !r.Hidden && t.Kind == "question" {
				accepted = true
			}
		}
		if !accepted {
			return bad()
		}
	}
	if len(replies) > MaxReplies {
		return bad()
	}
	keys := map[string]bool{}
	for _, r := range c.Receipts {
		k := fmt.Sprintf("%d/%s", r.ActorID, r.Key)
		if !keyPattern.MatchString(r.Key) || r.ActorID <= 0 || len(r.Digest) != 64 || r.Result.Slug != c.Slug || r.Result.Version < 1 || r.Result.Version > c.Version || keys[k] {
			return bad()
		}
		keys[k] = true
	}
	return nil
}

func DecodeCircle(b []byte) (Circle, error) {
	var c Circle
	// JSONB adds insignificant whitespace. Enforce the same compact budget
	// used for writes rather than rejecting a successfully stored aggregate.
	if len(b) > 2*MaxCircleBytes {
		return c, ErrCapacity
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, b); err != nil {
		return c, ErrInvalid
	}
	if compact.Len() > MaxCircleBytes {
		return c, ErrCapacity
	}
	if err := decode(compact.Bytes(), &c); err != nil {
		return c, err
	}
	return c, Validate(c)
}
