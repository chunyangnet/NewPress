package community

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/audit"
)

const (
	MaxAttachments           = 256
	MaxAttachmentsPerPost    = 8
	MaxAttachmentBytes       = 10 << 20
	MaxCircleAttachmentBytes = 128 << 20
	MaxReports               = 500
	MaxChanges               = 500
	MaxActivities            = 2048
)

type Attachment struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	AuthorID  int64     `json:"author_id"`
	TopicID   int64     `json:"topic_id,omitempty"`
	ReplyID   int64     `json:"reply_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Report struct {
	ID         int64     `json:"id"`
	UserID     int64     `json:"user_id"`
	TopicID    int64     `json:"topic_id"`
	ReplyID    int64     `json:"reply_id,omitempty"`
	Category   string    `json:"category"`
	Reason     string    `json:"reason"`
	Status     string    `json:"status"`
	Note       string    `json:"note,omitempty"`
	ReviewerID int64     `json:"reviewer_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	ReviewedAt time.Time `json:"reviewed_at,omitempty"`
}

type Change struct {
	Version int64     `json:"version"`
	ActorID int64     `json:"actor_id"`
	Action  string    `json:"action"`
	TopicID int64     `json:"topic_id,omitempty"`
	ReplyID int64     `json:"reply_id,omitempty"`
	UserID  int64     `json:"user_id,omitempty"`
	At      time.Time `json:"at"`
}

func TopicByID(c Circle, id int64) (Topic, bool) {
	for _, t := range c.Topics {
		if t.ID == id {
			return t, true
		}
	}
	return Topic{}, false
}
func ReplyByID(t Topic, id int64) (Reply, bool) {
	for _, r := range t.Replies {
		if r.ID == id {
			return r, true
		}
	}
	return Reply{}, false
}
func AttachmentByID(c Circle, id string) (Attachment, bool) {
	for _, file := range c.Attachments {
		if file.ID == id {
			return file, true
		}
	}
	return Attachment{}, false
}
func CanReadAttachment(c Circle, file Attachment, a Actor) bool {
	if file.TopicID == 0 {
		return a.ID > 0 && (a.ID == file.AuthorID || Manages(c, a))
	}
	if !CanRead(c, a) {
		return false
	}
	t, ok := TopicByID(c, file.TopicID)
	if !ok || t.Hidden && !Manages(c, a) {
		return false
	}
	if file.ReplyID == 0 {
		return slices.Contains(t.Attachments, file.ID)
	}
	r, ok := ReplyByID(t, file.ReplyID)
	return ok && (!r.Hidden || Manages(c, a)) && slices.Contains(r.Attachments, file.ID)
}
func validFormat(format string) bool {
	return format == "" || format == "plain" || format == "markdown"
}

func bindAttachments(c *Circle, a Actor, ids []string, topic, reply int64) error {
	if len(ids) > MaxAttachmentsPerPost {
		return ErrCapacity
	}
	seen := map[string]bool{}
	for _, id := range ids {
		file, ok := AttachmentByID(*c, id)
		if !ok || seen[id] {
			return ErrInvalid
		}
		if file.AuthorID != a.ID && !Manages(*c, a) || file.TopicID != 0 && (file.TopicID != topic || file.ReplyID != reply) {
			return ErrForbidden
		}
		seen[id] = true
	}
	for i := range c.Attachments {
		file := &c.Attachments[i]
		if seen[file.ID] {
			file.TopicID, file.ReplyID = topic, reply
		} else if file.TopicID == topic && file.ReplyID == reply {
			file.TopicID, file.ReplyID = 0, 0
		}
	}
	return nil
}

func mutateFeature(c *Circle, a Actor, cmd Command, result *Result, now time.Time) error {
	switch cmd.Action {
	case "archive_circle":
		if !Owns(*c, a) {
			return ErrForbidden
		}
		if cmd.Enabled == nil {
			return ErrInvalid
		}
		c.Archived = *cmd.Enabled
	case "add_attachment":
		if !CanPost(*c, a) {
			return ErrForbidden
		}
		if cmd.Attachment == nil {
			return ErrInvalid
		}
		file := *cmd.Attachment
		if file.AuthorID != a.ID || file.TopicID != 0 || file.ReplyID != 0 || !file.CreatedAt.IsZero() {
			return ErrInvalid
		}
		file.CreatedAt = now.UTC()
		if !validAttachment(file) {
			return ErrInvalid
		}
		if _, ok := AttachmentByID(*c, file.ID); ok {
			return ErrConflict
		}
		var total int64
		for _, f := range c.Attachments {
			total += f.Size
		}
		if len(c.Attachments) >= MaxAttachments || total+file.Size > MaxCircleAttachmentBytes {
			return ErrCapacity
		}
		c.Attachments = append(c.Attachments, file)
		result.AttachmentID = file.ID
	case "remove_attachment":
		index := slices.IndexFunc(c.Attachments, func(f Attachment) bool { return f.ID == cmd.AttachmentID })
		if index < 0 {
			return ErrNotFound
		}
		file := c.Attachments[index]
		if file.AuthorID != a.ID && !Manages(*c, a) {
			return ErrForbidden
		}
		if file.TopicID != 0 {
			return ErrConflict
		}
		c.Attachments = slices.Delete(c.Attachments, index, index+1)
	case "report":
		if a.ID <= 0 || !CanRead(*c, a) || Role(*c, a) == "banned" {
			return ErrForbidden
		}
		t, ok := TopicByID(*c, cmd.TopicID)
		if !ok || t.Hidden && !Manages(*c, a) {
			return ErrNotFound
		}
		if cmd.ReplyID > 0 {
			r, found := ReplyByID(t, cmd.ReplyID)
			if !found || r.Hidden && !Manages(*c, a) {
				return ErrNotFound
			}
		}
		if cmd.ReplyID < 0 || !slices.Contains([]string{"spam", "harassment", "privacy", "illegal", "other"}, cmd.Category) || !textValid(cmd.Reason, 1, 1000, true) {
			return ErrInvalid
		}
		open := 0
		for _, r := range c.Reports {
			if r.UserID == a.ID && r.Status == "open" {
				open++
				if r.TopicID == cmd.TopicID && r.ReplyID == cmd.ReplyID {
					return ErrConflict
				}
			}
		}
		if len(c.Reports) >= MaxReports || open >= 10 {
			return ErrCapacity
		}
		if c.NextReport == 0 {
			c.NextReport = 1
		}
		result.ReportID = c.NextReport
		c.NextReport++
		c.Reports = append(c.Reports, Report{ID: result.ReportID, UserID: a.ID, TopicID: cmd.TopicID, ReplyID: cmd.ReplyID, Category: cmd.Category, Reason: cmd.Reason, Status: "open", CreatedAt: now.UTC()})
	case "review_report":
		if !Manages(*c, a) {
			return ErrForbidden
		}
		if !slices.Contains([]string{"resolved", "dismissed"}, cmd.Status) || !textValid(cmd.Note, 1, 1000, true) {
			return ErrInvalid
		}
		i := slices.IndexFunc(c.Reports, func(r Report) bool { return r.ID == cmd.ReportID })
		if i < 0 {
			return ErrNotFound
		}
		report := &c.Reports[i]
		if report.Status != "open" {
			return ErrConflict
		}
		if cmd.Enabled != nil && *cmd.Enabled {
			if cmd.Status != "resolved" {
				return ErrInvalid
			}
			ti := slices.IndexFunc(c.Topics, func(t Topic) bool { return t.ID == report.TopicID })
			if ti < 0 {
				return ErrNotFound
			}
			t := &c.Topics[ti]
			if report.ReplyID == 0 {
				t.Hidden = true
			} else {
				ri := slices.IndexFunc(t.Replies, func(r Reply) bool { return r.ID == report.ReplyID })
				if ri < 0 {
					return ErrNotFound
				}
				t.Replies[ri].Hidden = true
				if t.AcceptedID == report.ReplyID {
					t.AcceptedID = 0
				}
			}
		}
		report.Status, report.Note, report.ReviewerID, report.ReviewedAt = cmd.Status, cmd.Note, a.ID, now.UTC()
		result.ReportID = report.ID
	case "clear_reports":
		if !Owns(*c, a) {
			return ErrForbidden
		}
		c.Reports = slices.DeleteFunc(c.Reports, func(r Report) bool { return r.Status != "open" })
	default:
		return ErrInvalid
	}
	return nil
}

func mutateTopicLifecycle(c *Circle, a Actor, cmd Command, result *Result) error {
	if !Manages(*c, a) {
		return ErrForbidden
	}
	i := slices.IndexFunc(c.Topics, func(t Topic) bool { return t.ID == cmd.TopicID })
	if i < 0 {
		return ErrNotFound
	}
	t := &c.Topics[i]
	if cmd.Action == "archive_topic" {
		if cmd.Enabled == nil {
			return ErrInvalid
		}
		t.Archived = *cmd.Enabled
		return nil
	}
	if !Owns(*c, a) {
		return ErrForbidden
	}
	for _, r := range c.Reports {
		if r.Status == "open" && r.TopicID == cmd.TopicID && (cmd.Action == "delete_topic" || r.ReplyID == cmd.ReplyID) {
			return ErrConflict
		}
	}
	if cmd.Action == "delete_topic" {
		if !t.Hidden && !t.Archived {
			return ErrConflict
		}
		c.Topics = slices.Delete(c.Topics, i, i+1)
		c.Attachments = slices.DeleteFunc(c.Attachments, func(f Attachment) bool { return f.TopicID == cmd.TopicID })
		result.TopicID = 0
	} else {
		ri := slices.IndexFunc(t.Replies, func(r Reply) bool { return r.ID == cmd.ReplyID })
		if ri < 0 {
			return ErrNotFound
		}
		if !t.Replies[ri].Hidden {
			return ErrConflict
		}
		t.Replies = slices.Delete(t.Replies, ri, ri+1)
		if t.AcceptedID == cmd.ReplyID {
			t.AcceptedID = 0
		}
		c.Attachments = slices.DeleteFunc(c.Attachments, func(f Attachment) bool { return f.TopicID == cmd.TopicID && f.ReplyID == cmd.ReplyID })
	}
	return nil
}

func recordChange(c *Circle, a Actor, cmd Command, result Result, now time.Time) error {
	c.Changes = append(c.Changes, Change{Version: c.Version, ActorID: a.ID, Action: cmd.Action, TopicID: cmd.TopicID, ReplyID: cmd.ReplyID, UserID: cmd.UserID, At: now.UTC()})
	if len(c.Changes) > MaxChanges {
		c.Changes = slices.Clone(c.Changes[len(c.Changes)-MaxChanges:])
	}
	e := activity.Event{Kind: activity.Community, Key: fmt.Sprintf("community:%s:%d", c.Slug, c.Version), Circle: c.Slug, Version: c.Version, UserID: a.ID, TopicID: cmd.TopicID, ReplyID: cmd.ReplyID, Status: cmd.Action, At: now.UTC(), Due: now.UTC()}
	add := func(id int64) {
		if id > 0 && id != a.ID && !slices.Contains(e.Recipients, id) {
			e.Recipients = append(e.Recipients, id)
		}
	}
	managers := func() {
		add(c.OwnerID)
		for _, m := range c.Members {
			if m.Role == "moderator" {
				add(m.UserID)
			}
		}
	}
	switch cmd.Action {
	case "topic":
		e.TopicID = result.TopicID
		e.Mentions = activity.Mentions(cmd.Body)
		add(c.OwnerID)
		for _, m := range c.Members {
			if m.Role == "member" || m.Role == "moderator" {
				add(m.UserID)
			}
		}
	case "reply":
		e.ReplyID = result.ReplyID
		e.Mentions = activity.Mentions(cmd.Body)
		t, _ := TopicByID(*c, e.TopicID)
		add(t.AuthorID)
		for _, r := range t.Replies {
			if !r.Hidden {
				add(r.AuthorID)
			}
		}
	case "join":
		if Role(*c, a) != "pending" {
			return nil
		}
		managers()
	case "member", "remove_member", "transfer":
		add(cmd.UserID)
	case "report":
		managers()
	case "review_report":
		for _, r := range c.Reports {
			if r.ID == cmd.ReportID {
				add(r.UserID)
			}
		}
	case "accept":
		if cmd.ReplyID == 0 {
			return nil
		}
		t, _ := TopicByID(*c, cmd.TopicID)
		r, _ := ReplyByID(t, cmd.ReplyID)
		add(r.AuthorID)
	case "hide_topic":
		t, _ := TopicByID(*c, cmd.TopicID)
		add(t.AuthorID)
	case "hide_reply":
		t, _ := TopicByID(*c, cmd.TopicID)
		r, _ := ReplyByID(t, cmd.ReplyID)
		add(r.AuthorID)
	default:
		return nil
	}
	if len(e.Recipients) == 0 && len(e.Mentions) == 0 {
		return nil
	}
	if len(c.Activities) >= MaxActivities {
		return ErrCapacity
	}
	c.Activities = append(c.Activities, e)
	return nil
}

func AuditEvent(a Actor, cmd Command, result Result) audit.Event {
	object := "community_circle"
	id := result.Slug
	if cmd.TopicID > 0 || result.TopicID > 0 {
		object = "community_topic"
		id += ":" + strconv.FormatInt(max(cmd.TopicID, result.TopicID), 10)
	}
	return audit.Event{Action: "community." + cmd.Action, ObjectType: object, ObjectID: id, Actor: fmt.Sprintf("account:%d", a.ID), Metadata: map[string]any{"version": result.Version}}
}

func validateFeatures(c Circle) error {
	if c.NextReport < 0 || len(c.Attachments) > MaxAttachments || len(c.Reports) > MaxReports || len(c.Changes) > MaxChanges || len(c.Activities) > MaxActivities {
		return ErrInvalid
	}
	files := map[string]Attachment{}
	var total int64
	for _, f := range c.Attachments {
		if !validAttachment(f) {
			return ErrInvalid
		}
		if _, ok := files[f.ID]; ok {
			return ErrInvalid
		}
		files[f.ID] = f
		total += f.Size
	}
	if total > MaxCircleAttachmentBytes {
		return ErrCapacity
	}
	bound := map[string]bool{}
	check := func(format string, ids []string, topic, reply int64) bool {
		if !validFormat(format) || len(ids) > MaxAttachmentsPerPost {
			return false
		}
		for _, id := range ids {
			f, ok := files[id]
			if !ok || bound[id] || f.TopicID != topic || f.ReplyID != reply {
				return false
			}
			bound[id] = true
		}
		return true
	}
	for _, t := range c.Topics {
		if !check(t.Format, t.Attachments, t.ID, 0) {
			return ErrInvalid
		}
		for _, r := range t.Replies {
			if !check(r.Format, r.Attachments, t.ID, r.ID) {
				return ErrInvalid
			}
		}
	}
	for _, f := range c.Attachments {
		if f.TopicID != 0 && !bound[f.ID] || f.TopicID == 0 && f.ReplyID != 0 {
			return ErrInvalid
		}
	}
	reports := map[int64]bool{}
	for _, r := range c.Reports {
		if r.ID <= 0 || r.ID >= c.NextReport || reports[r.ID] || r.UserID <= 0 || r.TopicID <= 0 || r.ReplyID < 0 || !slices.Contains([]string{"spam", "harassment", "privacy", "illegal", "other"}, r.Category) || !textValid(r.Reason, 1, 1000, true) || !textValid(r.Note, 0, 1000, true) || !slices.Contains([]string{"open", "resolved", "dismissed"}, r.Status) || r.CreatedAt.IsZero() {
			return ErrInvalid
		}
		if r.Status == "open" {
			t, ok := TopicByID(c, r.TopicID)
			if !ok {
				return ErrInvalid
			}
			if r.ReplyID > 0 {
				if _, ok = ReplyByID(t, r.ReplyID); !ok {
					return ErrInvalid
				}
			}
			if r.ReviewerID != 0 || !r.ReviewedAt.IsZero() || r.Note != "" {
				return ErrInvalid
			}
		} else if r.ReviewerID <= 0 || r.ReviewedAt.Before(r.CreatedAt) || r.Note == "" {
			return ErrInvalid
		}
		reports[r.ID] = true
	}
	var previous int64
	for _, ch := range c.Changes {
		if ch.Version <= previous || ch.Version > c.Version || ch.ActorID <= 0 || ch.At.IsZero() || !validChangeAction(ch.Action) || ch.TopicID < 0 || ch.ReplyID < 0 || ch.UserID < 0 {
			return ErrInvalid
		}
		previous = ch.Version
	}
	if err := activity.Validate(c.Activities); err != nil {
		return ErrInvalid
	}
	for _, e := range c.Activities {
		if e.Kind != activity.Community || e.Circle != c.Slug || e.Version > c.Version {
			return ErrInvalid
		}
	}
	return nil
}

func validChangeAction(action string) bool {
	switch action {
	case "create", "settings", "join", "leave", "member", "remove_member", "transfer", "topic", "reply", "edit_topic", "edit_reply", "accept", "pin", "feature", "lock", "hide_topic", "hide_reply", "archive_circle", "archive_topic", "delete_topic", "delete_reply", "add_attachment", "remove_attachment", "report", "review_report", "clear_reports":
		return true
	}
	return false
}
