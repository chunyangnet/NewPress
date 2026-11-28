// Package questions owns global questions independently of circles and membership.
package questions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/activity"
)

var (
	ErrInvalid   = errors.New("invalid question command")
	ErrForbidden = errors.New("question operation forbidden")
	ErrNotFound  = errors.New("question not found")
	ErrConflict  = errors.New("question version or receipt conflict")
	ErrCapacity  = errors.New("question storage capacity exceeded")
)

const MaxBytes = 64 << 20

type Actor struct {
	ID    int64
	Admin bool
}
type Note struct {
	ID       int64     `json:"id"`
	AuthorID int64     `json:"author_id"`
	Body     string    `json:"body"`
	At       time.Time `json:"at"`
}
type Answer struct {
	ID        int64     `json:"id"`
	AuthorID  int64     `json:"author_id"`
	Body      string    `json:"body"`
	Hidden    bool      `json:"hidden"`
	CreatedAt time.Time `json:"created_at"`
	EditedAt  time.Time `json:"edited_at"`
	Notes     []Note    `json:"notes,omitempty"`
}
type Report struct {
	ID       int64     `json:"id"`
	AuthorID int64     `json:"author_id"`
	AnswerID int64     `json:"answer_id"`
	Reason   string    `json:"reason"`
	Resolved bool      `json:"resolved"`
	At       time.Time `json:"at"`
}
type Question struct {
	ID         int64     `json:"id"`
	Version    int64     `json:"version"`
	AuthorID   int64     `json:"author_id"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Tags       []string  `json:"tags"`
	AcceptedID int64     `json:"accepted_id"`
	Hidden     bool      `json:"hidden"`
	Locked     bool      `json:"locked"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Answers    []Answer  `json:"answers"`
	Notes      []Note    `json:"notes,omitempty"`
	Reports    []Report  `json:"reports,omitempty"`
}
type Command struct {
	Action   string   `json:"action"`
	Key      string   `json:"key"`
	Version  int64    `json:"version"`
	AnswerID int64    `json:"answer_id"`
	ReportID int64    `json:"report_id"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Tags     []string `json:"tags"`
	Enabled  bool     `json:"enabled"`
}
type Result struct {
	ID       int64 `json:"id"`
	Version  int64 `json:"version"`
	AnswerID int64 `json:"answer_id,omitempty"`
	Replay   bool  `json:"replay,omitempty"`
}
type Receipt struct {
	Actor  int64  `json:"actor"`
	Key    string `json:"key"`
	Digest string `json:"digest"`
	Result Result `json:"result"`
}
type State struct {
	Schema   int              `json:"schema"`
	NextID   int64            `json:"next_id"`
	Items    []Question       `json:"items"`
	Receipts []Receipt        `json:"receipts"`
	Events   []activity.Event `json:"events"`
}
type Store interface {
	Ready(context.Context) error
	Snapshot(context.Context) (State, error)
	Apply(context.Context, int64, Actor, Command, time.Time) (Result, error)
	activity.Source
}

func Empty() State { return State{Schema: 1, NextID: 1, Items: []Question{}} }
func Clone(s State) State {
	s.Items = slices.Clone(s.Items)
	for i := range s.Items {
		q := &s.Items[i]
		q.Tags = slices.Clone(q.Tags)
		q.Answers = slices.Clone(q.Answers)
		for j := range q.Answers {
			q.Answers[j].Notes = slices.Clone(q.Answers[j].Notes)
		}
		q.Notes = slices.Clone(q.Notes)
		q.Reports = slices.Clone(q.Reports)
	}
	s.Receipts = slices.Clone(s.Receipts)
	s.Events = slices.Clone(s.Events)
	for i := range s.Events {
		s.Events[i].Mentions = slices.Clone(s.Events[i].Mentions)
		s.Events[i].Recipients = slices.Clone(s.Events[i].Recipients)
	}
	return s
}
func URL(id int64) string { return fmt.Sprintf("/questions/%d/", id) }
func Get(s State, id int64) (Question, bool) {
	for _, q := range s.Items {
		if q.ID == id {
			return q, true
		}
	}
	return Question{}, false
}
func text(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && !strings.ContainsRune(s, 0)
}
func normalizeTags(tags []string) ([]string, error) {
	out := []string{}
	if len(tags) > 5 {
		return nil, ErrInvalid
	}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if !text(tag, 24) || strings.ContainsAny(tag, "\r\n\t,#") {
			return nil, ErrInvalid
		}
		if !slices.Contains(out, tag) {
			out = append(out, tag)
		}
	}
	return out, nil
}
func Apply(current State, id int64, a Actor, c Command, now time.Time) (State, Result, error) {
	fail := func(e error) (State, Result, error) { return current, Result{}, e }
	if a.ID <= 0 {
		return fail(ErrForbidden)
	}
	if !text(c.Key, 100) || len(c.Key) < 8 || now.IsZero() || !validCommand(c) {
		return fail(ErrInvalid)
	}
	b, _ := json.Marshal(struct {
		ID int64
		C  Command
	}{id, c})
	h := sha256.Sum256(b)
	digest := hex.EncodeToString(h[:])
	for _, r := range current.Receipts {
		if r.Actor == a.ID && r.Key == c.Key {
			if r.Digest != digest {
				return fail(ErrConflict)
			}
			result := r.Result
			result.Replay = true
			return current, result, nil
		}
	}
	s := Clone(current)
	now = now.UTC()
	index := -1
	for i := range s.Items {
		if s.Items[i].ID == id {
			index = i
			break
		}
	}
	if c.Action == "create" {
		if id != 0 || c.Version != 0 {
			return fail(ErrInvalid)
		}
		if len(s.Items) >= 20000 {
			return fail(ErrCapacity)
		}
		if !text(c.Title, 160) || !text(c.Body, 20000) {
			return fail(ErrInvalid)
		}
		tags, e := normalizeTags(c.Tags)
		if e != nil {
			return fail(e)
		}
		s.Items = append(s.Items, Question{ID: s.NextID, AuthorID: a.ID, Title: strings.TrimSpace(c.Title), Body: strings.TrimSpace(c.Body), Tags: tags, CreatedAt: now})
		s.NextID++
		index = len(s.Items) - 1
	} else {
		if index < 0 {
			return fail(ErrNotFound)
		}
		if s.Items[index].Hidden && !a.Admin {
			return fail(ErrNotFound)
		}
		if c.Version != s.Items[index].Version {
			return fail(ErrConflict)
		}
	}
	q := &s.Items[index]
	result := Result{ID: q.ID}
	recipients := []int64{}
	status := ""
	answerIndex := -1
	for i := range q.Answers {
		if q.Answers[i].ID == c.AnswerID {
			answerIndex = i
			break
		}
	}
	if q.Locked && !a.Admin && c.Action != "report" {
		return fail(ErrForbidden)
	}
	switch c.Action {
	case "create":
	case "edit":
		if q.AuthorID != a.ID && !a.Admin {
			return fail(ErrForbidden)
		}
		if !text(c.Title, 160) || !text(c.Body, 20000) {
			return fail(ErrInvalid)
		}
		tags, e := normalizeTags(c.Tags)
		if e != nil {
			return fail(e)
		}
		q.Title = strings.TrimSpace(c.Title)
		q.Body = strings.TrimSpace(c.Body)
		q.Tags = tags
	case "answer":
		if !text(c.Body, 12000) {
			return fail(ErrInvalid)
		}
		if len(q.Answers) >= 2000 {
			return fail(ErrCapacity)
		}
		result.AnswerID = int64(len(q.Answers) + 1)
		q.Answers = append(q.Answers, Answer{ID: result.AnswerID, AuthorID: a.ID, Body: strings.TrimSpace(c.Body), CreatedAt: now})
		status = "answer"
		recipients = []int64{q.AuthorID}
	case "edit_answer":
		if answerIndex < 0 || q.Answers[answerIndex].Hidden {
			return fail(ErrNotFound)
		}
		answer := &q.Answers[answerIndex]
		if answer.AuthorID != a.ID && !a.Admin {
			return fail(ErrForbidden)
		}
		if !text(c.Body, 12000) {
			return fail(ErrInvalid)
		}
		answer.Body = strings.TrimSpace(c.Body)
		answer.EditedAt = now
		result.AnswerID = answer.ID
		if q.AcceptedID == answer.ID {
			q.AcceptedID = 0
		}
	case "note":
		if !text(c.Body, 2000) {
			return fail(ErrInvalid)
		}
		notes := &q.Notes
		recipients = []int64{q.AuthorID}
		if c.AnswerID > 0 {
			if answerIndex < 0 || q.Answers[answerIndex].Hidden {
				return fail(ErrNotFound)
			}
			notes = &q.Answers[answerIndex].Notes
			recipients = append(recipients, q.Answers[answerIndex].AuthorID)
			result.AnswerID = c.AnswerID
		}
		if len(*notes) >= 100 {
			return fail(ErrCapacity)
		}
		*notes = append(*notes, Note{ID: int64(len(*notes) + 1), AuthorID: a.ID, Body: strings.TrimSpace(c.Body), At: now})
		status = "note"
	case "accept":
		if q.AuthorID != a.ID {
			return fail(ErrForbidden)
		}
		if c.AnswerID < 0 {
			return fail(ErrInvalid)
		}
		if c.AnswerID > 0 {
			if answerIndex < 0 || q.Answers[answerIndex].Hidden {
				return fail(ErrNotFound)
			}
			recipients = []int64{q.Answers[answerIndex].AuthorID}
			status = "accept"
		}
		q.AcceptedID = c.AnswerID
		result.AnswerID = c.AnswerID
	case "hide", "lock", "hide_answer":
		if !a.Admin {
			return fail(ErrForbidden)
		}
		if c.Action == "hide" {
			q.Hidden = c.Enabled
		} else if c.Action == "lock" {
			q.Locked = c.Enabled
		} else {
			if answerIndex < 0 {
				return fail(ErrNotFound)
			}
			q.Answers[answerIndex].Hidden = c.Enabled
			if c.Enabled && q.AcceptedID == c.AnswerID {
				q.AcceptedID = 0
			}
		}
		status = "moderation"
		recipients = []int64{q.AuthorID}
		if c.Action == "hide_answer" {
			recipients = []int64{q.Answers[answerIndex].AuthorID}
		}
	case "report":
		if !text(c.Body, 1000) {
			return fail(ErrInvalid)
		}
		if c.AnswerID < 0 {
			return fail(ErrInvalid)
		}
		if c.AnswerID > 0 && (answerIndex < 0 || q.Answers[answerIndex].Hidden) {
			return fail(ErrNotFound)
		}
		if len(q.Reports) >= 200 {
			return fail(ErrCapacity)
		}
		q.Reports = append(q.Reports, Report{ID: int64(len(q.Reports) + 1), AuthorID: a.ID, AnswerID: c.AnswerID, Reason: strings.TrimSpace(c.Body), At: now})
	case "resolve_report":
		if !a.Admin {
			return fail(ErrForbidden)
		}
		found := false
		for i := range q.Reports {
			if q.Reports[i].ID == c.ReportID {
				q.Reports[i].Resolved = true
				recipients = []int64{q.Reports[i].AuthorID}
				found = true
			}
		}
		if !found {
			return fail(ErrNotFound)
		}
		status = "moderation"
	default:
		return fail(ErrInvalid)
	}
	q.Version++
	q.UpdatedAt = now
	result.Version = q.Version
	if status != "" {
		unique := []int64{}
		for _, uid := range recipients {
			if uid > 0 && !slices.Contains(unique, uid) {
				unique = append(unique, uid)
			}
		}
		e := activity.Event{Key: fmt.Sprintf("questions:%d:%d", q.ID, q.Version), Kind: "questions", ObjectID: q.ID, UserID: a.ID, Version: q.Version, Status: status, ReplyID: result.AnswerID, Recipients: unique, At: now, Due: now}
		s.Events = append(s.Events, e)
	}
	if len(s.Events) > 20000 {
		return fail(ErrCapacity)
	}
	s.Receipts = append(s.Receipts, Receipt{a.ID, c.Key, digest, result})
	if len(s.Receipts) > 20000 {
		s.Receipts = s.Receipts[len(s.Receipts)-20000:]
	}
	return s, result, nil
}

func Decode(b []byte) (State, error) {
	s := Empty()
	if len(b) > MaxBytes {
		return s, ErrCapacity
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.Schema != 1 || s.NextID < 1 || len(s.Items) > 20000 || len(s.Events) > 20000 || len(s.Receipts) > 20000 {
		return s, ErrInvalid
	}
	validNotes := func(notes []Note) bool {
		if len(notes) > 100 {
			return false
		}
		for i, n := range notes {
			if n.ID != int64(i+1) || n.AuthorID <= 0 || !text(n.Body, 2000) || n.At.IsZero() {
				return false
			}
		}
		return true
	}
	// Reuse the identity index for receipt and event references; repeatedly
	// scanning all questions makes loading a large state quadratic.
	questionsByID := make(map[int64]*Question, len(s.Items))
	for i := range s.Items {
		q := &s.Items[i]
		if q.ID <= 0 || q.ID >= s.NextID || questionsByID[q.ID] != nil || q.Version < 1 || q.AuthorID <= 0 || !text(q.Title, 160) || !text(q.Body, 20000) {
			return s, ErrInvalid
		}
		questionsByID[q.ID] = q
		tags, err := normalizeTags(q.Tags)
		if err != nil || !slices.Equal(tags, q.Tags) || q.CreatedAt.IsZero() || q.UpdatedAt.Before(q.CreatedAt) || len(q.Answers) > 2000 || len(q.Reports) > 200 || !validNotes(q.Notes) {
			return s, ErrInvalid
		}
		for i, report := range q.Reports {
			if report.ID != int64(i+1) || report.AuthorID <= 0 || report.AnswerID < 0 || report.AnswerID > int64(len(q.Answers)) || !text(report.Reason, 1000) || report.At.IsZero() {
				return s, ErrInvalid
			}
		}
		accepted := q.AcceptedID == 0
		for i, a := range q.Answers {
			if a.ID != int64(i+1) || a.AuthorID <= 0 || !text(a.Body, 12000) || a.CreatedAt.IsZero() || !validNotes(a.Notes) {
				return s, ErrInvalid
			}
			if a.ID == q.AcceptedID && !a.Hidden {
				accepted = true
			}
		}
		if !accepted {
			return s, ErrInvalid
		}
	}
	keys := map[string]bool{}
	for _, r := range s.Receipts {
		key := fmt.Sprintf("%d:%s", r.Actor, r.Key)
		q, found := questionsByID[r.Result.ID]
		digest, err := hex.DecodeString(r.Digest)
		if r.Actor <= 0 || !text(r.Key, 100) || len(r.Key) < 8 || keys[key] || err != nil || len(digest) != sha256.Size || !found || r.Result.Version < 1 || r.Result.Version > q.Version || r.Result.AnswerID < 0 || r.Result.AnswerID > int64(len(q.Answers)) || r.Result.Replay {
			return s, ErrInvalid
		}
		keys[key] = true
	}
	for _, event := range s.Events {
		q, found := questionsByID[event.ObjectID]
		if event.Kind != "questions" || !found || event.Version > q.Version || event.ReplyID > int64(len(q.Answers)) {
			return s, ErrInvalid
		}
	}
	if err := activity.Validate(s.Events); err != nil {
		return s, err
	}
	return s, nil
}
