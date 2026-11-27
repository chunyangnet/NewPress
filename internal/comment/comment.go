// Package comment owns public discussion records, moderation state and the
// persistence contract shared by the local file and PostgreSQL stores.
package comment

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusSpam     = "spam"
	StatusTrash    = "trash"

	MaxAuthorNameRunes  = 160
	MaxAuthorEmailBytes = 320
	MaxContentRunes     = 5_000
	MaxContentBytes     = 12 << 10
	MaxUserAgentBytes   = 512
	MaxSearchRunes      = 100
	MaxThreadDepth      = 5
	MaxListLimit        = 200
	MaxBulkMutations    = 100
	MaxRevisionLimit    = 100
)

var (
	ErrNotFound         = errors.New("comment not found")
	ErrRevisionNotFound = errors.New("comment revision not found")
	ErrVersionConflict  = errors.New("comment version conflict")
	ErrInvalidParent    = errors.New("comment parent is invalid")
	ErrInvalidInput     = errors.New("comment input is invalid")
)

type Comment struct {
	ID            int64     `json:"id"`
	PostID        int64     `json:"post_id"`
	ParentID      int64     `json:"parent_id,omitempty"`
	UserID        int64     `json:"user_id,omitempty"`
	Version       int64     `json:"version"`
	AuthorName    string    `json:"author_name"`
	AuthorEmail   string    `json:"author_email"`
	Status        string    `json:"status"`
	ContentSource string    `json:"content_source"`
	ContentHTML   string    `json:"content_html"`
	AuthorIPHash  string    `json:"author_ip_hash,omitempty"`
	UserAgent     string    `json:"user_agent,omitempty"`
	StaffReply    bool      `json:"staff_reply,omitempty"`
	CreatedBy     string    `json:"created_by,omitempty"`
	EditedBy      string    `json:"edited_by,omitempty"`
	EditedAt      time.Time `json:"edited_at,omitempty"`
	ModeratedBy   string    `json:"moderated_by,omitempty"`
	ModeratedAt   time.Time `json:"moderated_at,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type CreateInput struct {
	PostID        int64
	ParentID      int64
	UserID        int64
	AuthorName    string
	AuthorEmail   string
	Status        string
	ContentSource string
	AuthorIPHash  string
	UserAgent     string
}

type Query struct {
	PostID  int64
	UserID  int64
	OwnOnly bool
	Status  string
	Search  string
	Limit   int
	Offset  int
}

type List struct {
	Items        []Comment
	Total        int
	StatusCounts map[string]int
}

type Snapshot struct {
	Total        int
	StatusCounts map[string]int
	UpdatedAt    time.Time
}

type Mutation struct {
	ExpectedVersion int64
	Actor           string
}

// EditInput contains only the author-visible fields an administrator may
// correct. Post/thread ownership, source fingerprints, user identity and
// moderation state remain immutable through the edit operation.
type EditInput struct {
	AuthorName    string
	AuthorEmail   string
	ContentSource string
}

// ReplyInput creates a trusted staff response beneath an existing comment.
// The store derives post ID and moderation state from the locked parent so a
// client cannot move a reply to another thread or publish beneath an isolated
// parent. ExpectedParentVersion protects the parent/status/depth decision.
type ReplyInput struct {
	ParentID              int64
	ExpectedParentVersion int64
	UserID                int64
	AuthorName            string
	AuthorEmail           string
	ContentSource         string
	Actor                 string
}

// Revision is the bounded, body-preserving audit snapshot captured immediately
// before a safe administrative edit. Actor and CreatedAt describe the edit
// that advanced this snapshot to the next version.
type Revision struct {
	CommentID     int64     `json:"comment_id"`
	Version       int64     `json:"version"`
	AuthorName    string    `json:"author_name"`
	AuthorEmail   string    `json:"author_email"`
	Status        string    `json:"status"`
	ContentSource string    `json:"content_source"`
	ContentHTML   string    `json:"content_html"`
	Actor         string    `json:"actor"`
	CreatedAt     time.Time `json:"created_at"`
}

// BulkMutation binds a comment ID to the exact canonical version selected by
// an administrator. Keeping the pair together prevents independently ordered
// form fields from being zipped into the wrong optimistic-lock precondition.
type BulkMutation struct {
	ID              int64
	ExpectedVersion int64
}

// BulkStatusChange is an all-or-nothing moderation transition. Store
// implementations must validate the complete selection before the first
// write, then update every comment and its moderation metadata under one file
// lock or PostgreSQL transaction.
type BulkStatusChange struct {
	Items  []BulkMutation
	Status string
	Actor  string
}

type Store interface {
	Ready(context.Context) error
	ListApproved(context.Context, int64, int, int) ([]Comment, int, error)
	ListAdmin(context.Context, Query) (List, error)
	Get(context.Context, int64) (Comment, error)
	Create(context.Context, CreateInput) (Comment, error)
	FindSubmission(context.Context, string, string) (SubmissionResult, error)
	CreateOnce(context.Context, Submission) (SubmissionResult, error)
	Edit(context.Context, int64, Mutation, EditInput) (Comment, error)
	RestoreRevision(context.Context, int64, int64, Mutation) (Comment, error)
	Reply(context.Context, ReplyInput) (Comment, error)
	ListRevisions(context.Context, int64, int) ([]Revision, error)
	ChangeStatus(context.Context, int64, Mutation, string) (Comment, error)
	BulkChangeStatus(context.Context, BulkStatusChange) ([]Comment, error)
	Delete(context.Context, int64, Mutation) error
	Snapshot(context.Context) (Snapshot, error)
}

type ValidationError struct {
	Fields map[string]string
}

func (err *ValidationError) Error() string { return "comment validation failed" }

func FieldErrors(err error) map[string]string {
	var validation *ValidationError
	if !errors.As(err, &validation) {
		return nil
	}
	result := make(map[string]string, len(validation.Fields))
	for key, value := range validation.Fields {
		result[key] = value
	}
	return result
}

func NormalizeCreateInput(input CreateInput) (CreateInput, map[string]string) {
	fields := make(map[string]string)
	if !utf8.ValidString(input.AuthorEmail) {
		fields["author_email"] = "邮箱地址必须是有效的 UTF-8 文本"
		return input, fields
	}
	input.AuthorName = strings.TrimSpace(input.AuthorName)
	input.AuthorEmail = strings.ToLower(strings.TrimSpace(input.AuthorEmail))
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.ContentSource = normalizeLineEndings(input.ContentSource)
	input.ContentSource = strings.TrimSpace(input.ContentSource)
	input.AuthorIPHash = strings.ToLower(strings.TrimSpace(input.AuthorIPHash))
	input.UserAgent = strings.TrimSpace(input.UserAgent)
	if input.Status == "" {
		input.Status = StatusPending
	}

	if input.PostID <= 0 {
		fields["post_id"] = "评论必须关联有效内容"
	}
	if input.ParentID < 0 {
		fields["parent_id"] = "回复目标不正确"
	}
	if input.UserID < 0 {
		fields["user_id"] = "评论用户标识不正确"
	}
	if !validText(input.AuthorName, 1, MaxAuthorNameRunes, false) {
		fields["author_name"] = fmt.Sprintf("称呼需为 1–%d 个字符且不能包含控制字符", MaxAuthorNameRunes)
	}
	if len(input.AuthorEmail) > MaxAuthorEmailBytes || !validEmail(input.AuthorEmail) {
		fields["author_email"] = "请输入有效且不超过 320 字节的邮箱地址"
	}
	if !ValidStatus(input.Status) {
		fields["status"] = "评论状态不受支持"
	}
	if !validText(input.ContentSource, 1, MaxContentRunes, true) || len(input.ContentSource) > MaxContentBytes {
		fields["content_source"] = fmt.Sprintf("评论需为 1–%d 个字符且不超过 %d KiB", MaxContentRunes, MaxContentBytes>>10)
	}
	if input.AuthorIPHash != "" && !validHexDigest(input.AuthorIPHash) {
		fields["author_ip_hash"] = "来源摘要格式不正确"
	}
	if !utf8.ValidString(input.UserAgent) || len(input.UserAgent) > MaxUserAgentBytes || containsDisallowedControl(input.UserAgent, false) {
		fields["user_agent"] = "客户端标识超过上限或包含控制字符"
	}
	return input, fields
}

func NormalizeEditInput(input EditInput) (EditInput, map[string]string) {
	fields := make(map[string]string)
	if !utf8.ValidString(input.AuthorEmail) {
		fields["author_email"] = "邮箱地址必须是有效的 UTF-8 文本"
		return input, fields
	}
	input.AuthorName = strings.TrimSpace(input.AuthorName)
	input.AuthorEmail = strings.ToLower(strings.TrimSpace(input.AuthorEmail))
	input.ContentSource = strings.TrimSpace(normalizeLineEndings(input.ContentSource))
	if !validText(input.AuthorName, 1, MaxAuthorNameRunes, false) {
		fields["author_name"] = fmt.Sprintf("称呼需为 1–%d 个字符且不能包含控制字符", MaxAuthorNameRunes)
	}
	if len(input.AuthorEmail) > MaxAuthorEmailBytes || !validEmail(input.AuthorEmail) {
		fields["author_email"] = "请输入有效且不超过 320 字节的邮箱地址"
	}
	if !validText(input.ContentSource, 1, MaxContentRunes, true) || len(input.ContentSource) > MaxContentBytes {
		fields["content_source"] = fmt.Sprintf("评论需为 1–%d 个字符且不超过 %d KiB", MaxContentRunes, MaxContentBytes>>10)
	}
	return input, fields
}

func NormalizeReplyInput(input ReplyInput) (ReplyInput, map[string]string) {
	fields := make(map[string]string)
	input.Actor, _ = NormalizeActor(input.Actor)
	if input.ParentID <= 0 {
		fields["parent_id"] = "回复目标不正确"
	}
	if input.ExpectedParentVersion <= 0 {
		fields["parent_version"] = "回复目标版本不正确"
	}
	if input.UserID < 0 {
		fields["user_id"] = "回复用户标识不正确"
	}
	if _, ok := NormalizeActor(input.Actor); !ok {
		fields["actor"] = "回复者名称需为 1–160 个字符且不能包含控制字符"
	}
	normalized, editFields := NormalizeEditInput(EditInput{
		AuthorName: input.AuthorName, AuthorEmail: input.AuthorEmail, ContentSource: input.ContentSource,
	})
	input.AuthorName = normalized.AuthorName
	input.AuthorEmail = normalized.AuthorEmail
	input.ContentSource = normalized.ContentSource
	for key, message := range editFields {
		fields[key] = message
	}
	return input, fields
}

func NormalizeActor(actor string) (string, bool) {
	actor = strings.TrimSpace(actor)
	return actor, validText(actor, 1, 160, false)
}

func NormalizeRevisionLimit(limit int) int {
	if limit <= 0 || limit > MaxRevisionLimit {
		return 20
	}
	return limit
}

func NormalizeQuery(query Query) (Query, error) {
	query.Status = strings.ToLower(strings.TrimSpace(query.Status))
	query.Search = strings.TrimSpace(query.Search)
	if query.PostID < 0 || query.UserID < 0 || (query.OwnOnly && (query.UserID <= 0 || query.Status != "" && query.Status != StatusPending && query.Status != StatusApproved)) || (query.Status != "" && !ValidStatus(query.Status)) ||
		!utf8.ValidString(query.Search) || utf8.RuneCountInString(query.Search) > MaxSearchRunes || containsDisallowedControl(query.Search, false) || query.Offset < 0 {
		return Query{}, ErrInvalidInput
	}
	if query.Limit <= 0 || query.Limit > MaxListLimit {
		query.Limit = 20
	}
	return query, nil
}

// NormalizeBulkStatusChange validates and returns a deterministic ID-ordered
// copy. Stable ordering gives PostgreSQL callers a consistent row-lock order
// and keeps file/PostgreSQL result ordering identical.
func NormalizeBulkStatusChange(change BulkStatusChange) (BulkStatusChange, map[string]string) {
	fields := make(map[string]string)
	change.Status = strings.ToLower(strings.TrimSpace(change.Status))
	change.Actor = strings.TrimSpace(change.Actor)
	if !ValidStatus(change.Status) {
		fields["status"] = "评论状态不受支持"
	}
	if _, ok := NormalizeActor(change.Actor); change.Actor != "" && !ok {
		fields["actor"] = "审核者名称不能超过 160 个字符或包含控制字符"
	}
	if len(change.Items) == 0 {
		fields["selection"] = "至少选择一条评论"
	} else if len(change.Items) > MaxBulkMutations {
		fields["selection"] = fmt.Sprintf("单次最多审核 %d 条评论", MaxBulkMutations)
	}

	items := append([]BulkMutation(nil), change.Items...)
	sort.Slice(items, func(left, right int) bool { return items[left].ID < items[right].ID })
	for index, item := range items {
		if item.ID <= 0 || item.ExpectedVersion <= 0 {
			fields["selection"] = "所选评论包含无效编号或版本"
			break
		}
		if index > 0 && items[index-1].ID == item.ID {
			fields["selection"] = "同一条评论不能重复选择"
			break
		}
	}
	change.Items = items
	return change, fields
}

func ValidStatus(status string) bool {
	switch status {
	case StatusPending, StatusApproved, StatusSpam, StatusTrash:
		return true
	default:
		return false
	}
}

func StatusCounts(comments []Comment) map[string]int {
	counts := emptyStatusCounts()
	for _, item := range comments {
		if ValidStatus(item.Status) {
			counts[item.Status]++
		}
	}
	return counts
}

func SortPublic(items []Comment) {
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].CreatedAt.Equal(items[right].CreatedAt) {
			return items[left].ID < items[right].ID
		}
		return items[left].CreatedAt.Before(items[right].CreatedAt)
	})
}

func SortAdmin(items []Comment) {
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].UpdatedAt.Equal(items[right].UpdatedAt) {
			return items[left].ID > items[right].ID
		}
		return items[left].UpdatedAt.After(items[right].UpdatedAt)
	})
}

func RenderPlainText(source string) string {
	source = strings.TrimSpace(normalizeLineEndings(source))
	paragraphs := splitParagraphs(source)
	var builder strings.Builder
	for _, paragraph := range paragraphs {
		if paragraph == "" {
			continue
		}
		builder.WriteString("<p>")
		builder.WriteString(strings.ReplaceAll(html.EscapeString(paragraph), "\n", "<br>"))
		builder.WriteString("</p>")
	}
	return builder.String()
}

func emptyStatusCounts() map[string]int {
	return map[string]int{StatusPending: 0, StatusApproved: 0, StatusSpam: 0, StatusTrash: 0}
}

func normalizeLineEndings(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}

func splitParagraphs(value string) []string {
	lines := strings.Split(value, "\n")
	result := make([]string, 0, len(lines))
	current := make([]string, 0, 4)
	flush := func() {
		if len(current) == 0 {
			return
		}
		result = append(result, strings.Join(current, "\n"))
		current = current[:0]
	}
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	return result
}

func validText(value string, minimum, maximum int, allowNewline bool) bool {
	count := utf8.RuneCountInString(value)
	return utf8.ValidString(value) && count >= minimum && count <= maximum && !containsDisallowedControl(value, allowNewline)
}

func containsDisallowedControl(value string, allowNewline bool) bool {
	for _, character := range value {
		if !unicode.IsControl(character) {
			continue
		}
		if allowNewline && (character == '\n' || character == '\t') {
			continue
		}
		return true
	}
	return false
}

func validEmail(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && strings.EqualFold(parsed.Address, value) && strings.Contains(value, "@")
}

func validHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}
