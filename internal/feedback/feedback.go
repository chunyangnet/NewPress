// Package feedback implements configurable theme feature pages. Both storage
// adapters use this authorization and mutation engine.
package feedback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid   = errors.New("invalid feedback input")
	ErrForbidden = errors.New("feedback forbidden")
	ErrNotFound  = errors.New("feedback not found")
	ErrConflict  = errors.New("feedback version or request conflict")
	ErrCapacity  = errors.New("feedback capacity reached")
	ErrChanged   = errors.New("feedback file changed externally")
	slugPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	keyPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{16,100}$`)
)

const (
	MaxBoards        = 64
	MaxItems         = 2000
	MaxBoardBytes    = 8 << 20
	MaxDocumentBytes = 64 << 20
)

type Actor struct {
	ID    int64
	Roles []string
	Admin bool
}
type Grant struct {
	Role     string `json:"role"`
	Read     bool   `json:"read"`
	Submit   bool   `json:"submit"`
	Vote     bool   `json:"vote"`
	Comment  bool   `json:"comment"`
	Moderate bool   `json:"moderate"`
}
type Settings struct {
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Guidelines         string   `json:"guidelines"`
	SubmitLabel        string   `json:"submit_label"`
	Enabled            bool     `json:"enabled"`
	Submissions        bool     `json:"submissions"`
	Voting             bool     `json:"voting"`
	Comments           bool     `json:"comments"`
	OfficialReplies    bool     `json:"official_replies"`
	Roadmap            bool     `json:"roadmap"`
	History            bool     `json:"history"`
	Moderation         bool     `json:"moderation"`
	PrivateSubmissions bool     `json:"private_submissions"`
	DefaultView        string   `json:"default_view"`
	Accent             string   `json:"accent"`
	PageSize           int      `json:"page_size"`
	Kinds              []string `json:"kinds"`
	Categories         []string `json:"categories"`
	Permissions        []Grant  `json:"permissions,omitempty"`
}
type Reply struct {
	ID        int64     `json:"id"`
	AuthorID  int64     `json:"author_id"`
	Body      string    `json:"body"`
	Official  bool      `json:"official"`
	Hidden    bool      `json:"hidden"`
	CreatedAt time.Time `json:"created_at"`
}
type Event struct {
	ActorID int64     `json:"actor_id"`
	Status  string    `json:"status"`
	Note    string    `json:"note"`
	At      time.Time `json:"at"`
}
type Item struct {
	ID         int64     `json:"id"`
	AuthorID   int64     `json:"author_id"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Kind       string    `json:"kind"`
	Category   string    `json:"category"`
	Status     string    `json:"status"`
	Visibility string    `json:"visibility"`
	Private    bool      `json:"private"`
	Priority   string    `json:"priority"`
	Pinned     bool      `json:"pinned"`
	Locked     bool      `json:"locked"`
	Votes      []int64   `json:"votes"`
	Replies    []Reply   `json:"replies"`
	History    []Event   `json:"history"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type Receipt struct {
	ActorID int64  `json:"actor_id"`
	Key     string `json:"key"`
	Digest  string `json:"digest"`
	Result  Result `json:"result"`
}
type Board struct {
	Slug      string    `json:"slug"`
	Version   int64     `json:"version"`
	Settings  Settings  `json:"settings"`
	Items     []Item    `json:"items,omitempty"`
	Receipts  []Receipt `json:"receipts,omitempty"`
	NextItem  int64     `json:"next_item"`
	NextReply int64     `json:"next_reply"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Command struct {
	Action     string    `json:"action"`
	Key        string    `json:"key"`
	Version    int64     `json:"version"`
	Settings   *Settings `json:"settings,omitempty"`
	ItemID     int64     `json:"item_id,omitempty"`
	ReplyID    int64     `json:"reply_id,omitempty"`
	Title      string    `json:"title,omitempty"`
	Body       string    `json:"body,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	Category   string    `json:"category,omitempty"`
	Private    bool      `json:"private,omitempty"`
	Official   bool      `json:"official,omitempty"`
	Enabled    *bool     `json:"enabled,omitempty"`
	Status     string    `json:"status,omitempty"`
	Visibility string    `json:"visibility,omitempty"`
	Priority   string    `json:"priority,omitempty"`
}
type Result struct {
	Slug    string `json:"slug"`
	Version int64  `json:"version"`
	ItemID  int64  `json:"item_id,omitempty"`
	ReplyID int64  `json:"reply_id,omitempty"`
	Replay  bool   `json:"replay,omitempty"`
}
type Store interface {
	Ready(context.Context) error
	List(context.Context) ([]Board, error)
	Get(context.Context, string) (Board, error)
	Apply(context.Context, string, Actor, Command, time.Time) (Result, error)
}

func Defaults() Settings {
	return Settings{Name: "建议与反馈", Description: "好的改变，从一个想法开始。", Guidelines: "提交前先搜索已有反馈。请描述使用场景、遇到的问题和期待的结果；不要填写密码等敏感信息。", SubmitLabel: "提交反馈", Enabled: true, Submissions: true, Voting: true, Comments: true, OfficialReplies: true, Roadmap: true, History: true, PrivateSubmissions: true, DefaultView: "list", Accent: "blue", PageSize: 20, Kinds: []string{"suggestion", "issue", "feedback"}, Categories: []string{"使用体验", "功能需求", "内容与其他"}, Permissions: []Grant{{Role: "guest", Read: true}, {Role: "subscriber", Read: true, Submit: true, Vote: true, Comment: true}, {Role: "author", Read: true, Submit: true, Vote: true, Comment: true}, {Role: "editor", Read: true, Submit: true, Vote: true, Comment: true, Moderate: true}}}
}
func ValidSlug(s string) bool { return s != "new" && slugPattern.MatchString(s) }
func validText(s string, min, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(strings.TrimSpace(s)) < min || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}
func ValidKind(s string) bool { return slices.Contains([]string{"suggestion", "issue", "feedback"}, s) }
func ValidStatus(s string) bool {
	return slices.Contains([]string{"new", "planned", "progress", "done", "closed"}, s)
}
func ValidVisibility(s string) bool {
	return slices.Contains([]string{"public", "pending", "hidden"}, s)
}
func ValidSettings(c Settings) bool {
	if !validText(c.Name, 1, 60) || strings.ContainsAny(c.Name, "\n\t") || !validText(c.Description, 0, 300) || !validText(c.Guidelines, 0, 3000) || !validText(c.SubmitLabel, 1, 24) || strings.ContainsAny(c.SubmitLabel, "\n\t") {
		return false
	}
	if !slices.Contains([]string{"blue", "teal", "amber"}, c.Accent) || !slices.Contains([]string{"list", "roadmap"}, c.DefaultView) || (!c.Roadmap && c.DefaultView == "roadmap") || !slices.Contains([]int{10, 20, 40}, c.PageSize) || len(c.Kinds) < 1 || len(c.Kinds) > 3 || len(c.Categories) > 12 || len(c.Permissions) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, k := range c.Kinds {
		if !ValidKind(k) || seen[k] {
			return false
		}
		seen[k] = true
	}
	seen = map[string]bool{}
	for _, k := range c.Categories {
		if !validText(k, 1, 30) || strings.ContainsAny(k, "\n\t") || seen[k] {
			return false
		}
		seen[k] = true
	}
	seen = map[string]bool{}
	for _, g := range c.Permissions {
		if !ValidSlug(g.Role) || seen[g.Role] || g.Role == "administrator" || (!g.Read && (g.Submit || g.Vote || g.Comment || g.Moderate)) || g.Role == "guest" && (g.Submit || g.Vote || g.Comment || g.Moderate) {
			return false
		}
		seen[g.Role] = true
	}
	return true
}
func Can(b Board, a Actor, action string) bool {
	if a.ID > 0 && a.Admin {
		return true
	}
	if !b.Settings.Enabled {
		return false
	}
	for _, g := range b.Settings.Permissions {
		if !(a.ID == 0 && g.Role == "guest" || a.ID > 0 && slices.Contains(a.Roles, g.Role)) || !g.Read {
			continue
		}
		if action == "read" || a.ID > 0 && (action == "submit" && g.Submit || action == "vote" && g.Vote || action == "comment" && g.Comment || action == "moderate" && g.Moderate) {
			return true
		}
	}
	return false
}
func CanSee(b Board, a Actor, t Item) bool {
	return Can(b, a, "read") && (Can(b, a, "moderate") || t.Visibility != "hidden" && (t.AuthorID == a.ID || t.Visibility == "public" && !t.Private))
}
func CanEdit(b Board, a Actor, t Item) bool {
	return CanSee(b, a, t) && (Can(b, a, "moderate") || a.ID > 0 && a.ID == t.AuthorID && t.Status == "new" && !t.Locked && Can(b, a, "submit") && b.Settings.Submissions)
}
func Header(b Board) Board { b.Items = nil; b.Receipts = nil; return Clone(b) }
func Clone(b Board) Board  { v, _ := json.Marshal(b); var c Board; _ = json.Unmarshal(v, &c); return c }
func DecodeBoard(data []byte) (Board, error) {
	var b Board
	if len(data) > MaxBoardBytes {
		return b, ErrCapacity
	}
	if e := decode(data, &b); e != nil {
		return b, e
	}
	return b, Validate(b)
}
func Validate(b Board) error {
	if !ValidSlug(b.Slug) || b.Version < 1 || !ValidSettings(b.Settings) || b.NextItem < 1 || b.NextReply < 1 || b.CreatedAt.IsZero() || b.UpdatedAt.Before(b.CreatedAt) || len(b.Items) > MaxItems || len(b.Receipts) > 4096 {
		return ErrInvalid
	}
	ids, replies := map[int64]bool{}, map[int64]bool{}
	for _, t := range b.Items {
		if t.ID < 1 || t.ID >= b.NextItem || ids[t.ID] || t.AuthorID < 1 || !validText(t.Title, 3, 140) || !validText(t.Body, 5, 12000) || !ValidKind(t.Kind) || !validText(t.Category, 0, 30) || !ValidStatus(t.Status) || !ValidVisibility(t.Visibility) || !slices.Contains([]string{"low", "normal", "high", "urgent"}, t.Priority) || len(t.Votes) > 20000 || len(t.Replies) > 500 || len(t.History) > 200 || t.CreatedAt.IsZero() || t.UpdatedAt.Before(t.CreatedAt) {
			return ErrInvalid
		}
		ids[t.ID] = true
		voters := map[int64]bool{}
		for _, id := range t.Votes {
			if id < 1 || voters[id] {
				return ErrInvalid
			}
			voters[id] = true
		}
		for _, r := range t.Replies {
			if r.ID < 1 || r.ID >= b.NextReply || replies[r.ID] || r.AuthorID < 1 || !validText(r.Body, 1, 6000) || r.CreatedAt.IsZero() {
				return ErrInvalid
			}
			replies[r.ID] = true
		}
		for _, e := range t.History {
			if e.ActorID < 1 || !ValidStatus(e.Status) || !validText(e.Note, 0, 2000) || e.At.IsZero() {
				return ErrInvalid
			}
		}
	}
	keys := map[string]bool{}
	for _, r := range b.Receipts {
		k := stringID(r.ActorID) + ":" + r.Key
		if r.ActorID < 1 || !keyPattern.MatchString(r.Key) || len(r.Digest) != 64 || keys[k] || r.Result.Slug != b.Slug || r.Result.Version < 1 || r.Result.Version > b.Version || r.Result.Replay {
			return ErrInvalid
		}
		if _, e := hex.DecodeString(r.Digest); e != nil {
			return ErrInvalid
		}
		keys[k] = true
	}
	return nil
}
func stringID(id int64) string { b, _ := json.Marshal(id); return string(b) }

// Reject irrelevant command fields as well as unknown JSON fields in the HTTP
// decoder, so malformed requests cannot accidentally become a different action.
func validCommand(c Command) bool {
	if !keyPattern.MatchString(c.Key) || c.Version < 0 {
		return false
	}
	var fields []string
	switch c.Action {
	case "create", "configure":
		fields = []string{"settings"}
	case "submit":
		fields = []string{"title", "body", "kind", "category", "private"}
	case "edit":
		fields = []string{"item_id", "title", "body", "kind", "category"}
	case "reply":
		fields = []string{"item_id", "body", "official"}
	case "vote", "pin", "lock":
		fields = []string{"item_id", "enabled"}
	case "hide_reply":
		fields = []string{"item_id", "reply_id", "enabled"}
	case "status":
		fields = []string{"item_id", "status", "body"}
	case "visibility":
		fields = []string{"item_id", "visibility"}
	case "priority":
		fields = []string{"item_id", "priority"}
	default:
		return false
	}
	b, _ := json.Marshal(c)
	var values map[string]json.RawMessage
	_ = json.Unmarshal(b, &values)
	for k := range values {
		if k != "action" && k != "key" && k != "version" && !slices.Contains(fields, k) {
			return false
		}
	}
	return true
}

func Mutate(old *Board, slug string, a Actor, c Command, now time.Time) (Board, Result, error) {
	fail := func(e error) (Board, Result, error) { return Board{}, Result{}, e }
	if !ValidSlug(slug) || !validCommand(c) || now.IsZero() {
		return fail(ErrInvalid)
	}
	if a.ID <= 0 {
		return fail(ErrForbidden)
	}
	if c.Action == "create" || c.Action == "configure" {
		if !a.Admin {
			return fail(ErrForbidden)
		}
		if c.Settings == nil || !ValidSettings(*c.Settings) {
			return fail(ErrInvalid)
		}
	}
	var b Board
	if old == nil {
		if c.Action != "create" {
			return fail(ErrNotFound)
		}
		if c.Version != 0 {
			return fail(ErrConflict)
		}
		b = Board{Slug: slug, Settings: *c.Settings, NextItem: 1, NextReply: 1, CreatedAt: now}
	} else {
		b = Clone(*old)
	}
	if !Can(b, a, "read") {
		return fail(ErrForbidden)
	}
	idx := -1
	if c.ItemID > 0 {
		for i := range b.Items {
			if b.Items[i].ID == c.ItemID {
				idx = i
				break
			}
		}
		if idx < 0 || !CanSee(b, a, b.Items[idx]) {
			return fail(ErrNotFound)
		}
	}
	manage := Can(b, a, "moderate")
	switch c.Action {
	case "submit":
		if !b.Settings.Submissions || !Can(b, a, "submit") {
			return fail(ErrForbidden)
		}
	case "vote":
		if idx < 0 {
			return fail(ErrInvalid)
		}
		t := b.Items[idx]
		if !b.Settings.Voting || !Can(b, a, "vote") || t.Locked || t.Private || t.Visibility != "public" {
			return fail(ErrForbidden)
		}
	case "reply":
		if idx < 0 {
			return fail(ErrInvalid)
		}
		if c.Official {
			if !manage || !b.Settings.OfficialReplies {
				return fail(ErrForbidden)
			}
		} else if !b.Settings.Comments || !Can(b, a, "comment") || b.Items[idx].Locked {
			return fail(ErrForbidden)
		}
	case "edit":
		if idx < 0 {
			return fail(ErrInvalid)
		}
		if !CanEdit(b, a, b.Items[idx]) {
			return fail(ErrForbidden)
		}
	case "pin", "lock", "status", "visibility", "priority", "hide_reply":
		if idx < 0 {
			return fail(ErrInvalid)
		}
		if !manage {
			return fail(ErrForbidden)
		}
	}
	encoded, _ := json.Marshal(c)
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	for _, r := range b.Receipts {
		if r.ActorID == a.ID && r.Key == c.Key {
			if r.Digest != hash {
				return fail(ErrConflict)
			}
			result := r.Result
			result.Replay = true
			return b, result, nil
		}
	}
	if old != nil && c.Action == "create" {
		return fail(ErrConflict)
	}
	// Appends and desired-state votes are safe under the store's write lock;
	// destructive edits and configuration require the version the user reviewed.
	if old != nil && (c.Version < 1 || (c.Action != "submit" && c.Action != "reply" && c.Action != "vote" && c.Version != b.Version)) {
		return fail(ErrConflict)
	}
	result := Result{Slug: slug, ItemID: c.ItemID}
	switch c.Action {
	case "create", "configure":
		b.Settings = *c.Settings
		// Stored settings must not retain slices owned by the command's caller.
		b.Settings.Kinds = slices.Clone(c.Settings.Kinds)
		b.Settings.Categories = slices.Clone(c.Settings.Categories)
		b.Settings.Permissions = slices.Clone(c.Settings.Permissions)
	case "submit", "edit":
		if !validText(c.Title, 3, 140) || strings.ContainsAny(c.Title, "\n\t") || !validText(c.Body, 5, 12000) || !slices.Contains(b.Settings.Kinds, c.Kind) || (c.Category != "" && !slices.Contains(b.Settings.Categories, c.Category)) {
			return fail(ErrInvalid)
		}
		if c.Action == "submit" {
			if len(b.Items) >= MaxItems {
				return fail(ErrCapacity)
			}
			if c.Private && !b.Settings.PrivateSubmissions {
				return fail(ErrForbidden)
			}
			visibility := "public"
			if b.Settings.Moderation && !manage {
				visibility = "pending"
			}
			t := Item{ID: b.NextItem, AuthorID: a.ID, Title: strings.TrimSpace(c.Title), Body: strings.TrimSpace(c.Body), Kind: c.Kind, Category: c.Category, Status: "new", Priority: "normal", Visibility: visibility, Private: c.Private, CreatedAt: now, UpdatedAt: now, History: []Event{{ActorID: a.ID, Status: "new", At: now}}}
			b.Items = append(b.Items, t)
			b.NextItem++
			result.ItemID = t.ID
		} else {
			t := &b.Items[idx]
			t.Title = strings.TrimSpace(c.Title)
			t.Body = strings.TrimSpace(c.Body)
			t.Kind = c.Kind
			t.Category = c.Category
			if b.Settings.Moderation && !manage {
				t.Visibility = "pending"
			}
		}
	case "vote":
		if c.Enabled == nil {
			return fail(ErrInvalid)
		}
		t := &b.Items[idx]
		i := slices.Index(t.Votes, a.ID)
		if *c.Enabled && i < 0 {
			if len(t.Votes) >= 20000 {
				return fail(ErrCapacity)
			}
			t.Votes = append(t.Votes, a.ID)
		} else if !*c.Enabled && i >= 0 {
			t.Votes = slices.Delete(t.Votes, i, i+1)
		}
	case "reply":
		if !validText(c.Body, 1, 6000) {
			return fail(ErrInvalid)
		}
		t := &b.Items[idx]
		if len(t.Replies) >= 500 {
			return fail(ErrCapacity)
		}
		t.Replies = append(t.Replies, Reply{ID: b.NextReply, AuthorID: a.ID, Body: strings.TrimSpace(c.Body), Official: c.Official, CreatedAt: now})
		result.ReplyID = b.NextReply
		b.NextReply++
	case "status":
		if !ValidStatus(c.Status) || !validText(c.Body, 0, 2000) {
			return fail(ErrInvalid)
		}
		t := &b.Items[idx]
		if t.Status != c.Status {
			if len(t.History) >= 200 {
				return fail(ErrCapacity)
			}
			t.Status = c.Status
			t.History = append(t.History, Event{ActorID: a.ID, Status: c.Status, Note: strings.TrimSpace(c.Body), At: now})
		}
	case "visibility":
		if !ValidVisibility(c.Visibility) {
			return fail(ErrInvalid)
		}
		b.Items[idx].Visibility = c.Visibility
	case "priority":
		if !slices.Contains([]string{"low", "normal", "high", "urgent"}, c.Priority) {
			return fail(ErrInvalid)
		}
		b.Items[idx].Priority = c.Priority
	case "pin", "lock":
		if c.Enabled == nil {
			return fail(ErrInvalid)
		}
		if c.Action == "pin" {
			b.Items[idx].Pinned = *c.Enabled
		} else {
			b.Items[idx].Locked = *c.Enabled
		}
	case "hide_reply":
		if c.Enabled == nil || c.ReplyID <= 0 {
			return fail(ErrInvalid)
		}
		found := false
		for i := range b.Items[idx].Replies {
			if b.Items[idx].Replies[i].ID == c.ReplyID {
				b.Items[idx].Replies[i].Hidden = *c.Enabled
				found = true
				break
			}
		}
		if !found {
			return fail(ErrNotFound)
		}
	}
	if idx >= 0 {
		b.Items[idx].UpdatedAt = now
	}
	b.Version++
	b.UpdatedAt = now
	result.Version = b.Version
	b.Receipts = append(b.Receipts, Receipt{ActorID: a.ID, Key: c.Key, Digest: hash, Result: result})
	if len(b.Receipts) > 4096 {
		b.Receipts = b.Receipts[len(b.Receipts)-4096:]
	}
	if e := Validate(b); e != nil {
		return fail(e)
	}
	data, _ := json.Marshal(b)
	if len(data) > MaxBoardBytes {
		return fail(ErrCapacity)
	}
	return b, result, nil
}
