package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/contenthtml"
)

const (
	SnippetPrivate         = "private"
	SnippetShared          = "shared"
	SnippetWithdrawn       = "withdrawn"
	MaxSnippets            = 1000
	MaxSnippetRevisions    = 10000
	MaxSnippetBodyBytes    = 128 << 10
	MaxSnippetStorageBytes = 32 << 20
	MaxSnippetReceipts     = 10000
	MaxSnippetReceiptBytes = 8 << 20
	MaxSnippetResolve      = 100
)

var (
	ErrSnippetPermission  = errors.New("snippet permission denied")
	ErrSnippetCapacity    = errors.New("snippet capacity exceeded")
	ErrSnippetCorrupt     = errors.New("invalid stored snippet state")
	ErrSnippetReplay      = errors.New("snippet request identity conflict")
	ErrSnippetMaintenance = errors.New("snippet or editorial records are quarantined for offline maintenance")
)

// SnippetAccess is supplied by a trusted authentication boundary, never JSON.
// Full content writers manage shared objects. Own writers may update their
// articles; readers only receive revisions from the current sharing period.
type SnippetAccess struct {
	UserID                 int64
	Identity, Actor        string
	Read, Manage, WriteOwn bool
}

type Snippet struct {
	ID             int64     `json:"id"`
	Version        int64     `json:"version"`
	CurrentVersion int64     `json:"current_version"`
	SharedSince    int64     `json:"shared_since"`
	Visibility     string    `json:"visibility"`
	Title          string    `json:"title"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type SnippetRevision struct {
	Version       int64     `json:"version"`
	Title         string    `json:"title"`
	ContentSource string    `json:"content_source"`
	ContentHTML   string    `json:"content_html"`
	Actor         string    `json:"actor"`
	CreatedAt     time.Time `json:"created_at"`
}

type SnippetRecord struct {
	Snippet
	Revisions []SnippetRevision `json:"revisions"`
}

type SnippetRevisionInfo struct {
	Version   int64     `json:"version"`
	Title     string    `json:"title"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

type SnippetPage struct {
	Snippet
	Current    SnippetRevision       `json:"current"`
	History    []SnippetRevisionInfo `json:"history"`
	NextBefore int64                 `json:"next_before,omitempty"`
}

type SnippetInput struct {
	Title         string `json:"title"`
	ContentSource string `json:"content_source"`
	Visibility    string `json:"visibility"`
}

type SnippetList struct {
	Items []Snippet `json:"items"`
	Total int       `json:"total"`
}

type SnippetImpact struct {
	ID         int64   `json:"id"`
	Version    int64   `json:"version"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	Versions   []int64 `json:"reference_versions"`
	References int     `json:"references"`
	Changes    int     `json:"changes"`
}

type SnippetImpacts struct {
	Snippet Snippet         `json:"snippet"`
	Items   []SnippetImpact `json:"items"`
	Total   int             `json:"total"`
}

type SnippetSelection struct {
	ID      int64 `json:"id"`
	Version int64 `json:"version"`
}

type SnippetUpdate struct {
	SnippetID      int64              `json:"snippet_id"`
	SnippetVersion int64              `json:"snippet_version"`
	Items          []SnippetSelection `json:"items"`
	Digest         string             `json:"digest,omitempty"`
	RequestKey     string             `json:"request_key,omitempty"`
}

type SnippetPlan struct {
	Snippet Snippet         `json:"snippet"`
	Items   []SnippetImpact `json:"items"`
	Digest  string          `json:"digest"`
}

type SnippetReceipt struct {
	Identity       string             `json:"identity"`
	RequestKey     string             `json:"request_key"`
	Fingerprint    string             `json:"fingerprint"`
	SnippetID      int64              `json:"snippet_id"`
	SnippetVersion int64              `json:"snippet_version"`
	TargetVersion  int64              `json:"target_version"`
	Items          []SnippetSelection `json:"items"`
	CreatedAt      time.Time          `json:"created_at"`
}

type SnippetResult struct {
	Receipt  SnippetReceipt `json:"receipt"`
	Replayed bool           `json:"replayed"`
}

type SnippetRepository interface {
	PreviewSnippetDraft(context.Context, SnippetDraftRequest, SnippetAccess) (SnippetDraftPlan, error)
	ListSnippets(context.Context, SnippetAccess, int, int) (SnippetList, error)
	GetSnippet(context.Context, int64, SnippetAccess) (SnippetRecord, error)
	GetSnippetPage(context.Context, int64, SnippetAccess, int64, int) (SnippetPage, error)
	SaveSnippet(context.Context, int64, Mutation, SnippetInput, SnippetAccess) (Snippet, error)
	WithdrawSnippet(context.Context, int64, Mutation, SnippetAccess) (Snippet, error)
	ResolveSnippets(context.Context, []contenthtml.SnippetKey) (map[contenthtml.SnippetKey]string, error)
	SnippetImpacts(context.Context, int64, SnippetAccess, int, int) (SnippetImpacts, error)
	PreviewSnippetUpdate(context.Context, SnippetUpdate, SnippetAccess) (SnippetPlan, error)
	ApplySnippetUpdate(context.Context, SnippetUpdate, SnippetAccess) (SnippetResult, error)
}

func NormalizeSnippetInput(input SnippetInput) (SnippetInput, string, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.ContentSource = strings.TrimSpace(input.ContentSource)
	fields := map[string]string{}
	if !utf8.ValidString(input.Title) || input.Title == "" || utf8.RuneCountInString(input.Title) > MaxTitleRunes || containsControl(input.Title) {
		fields["title"] = "片段标题须为 1 至 200 个字符"
	}
	if !utf8.ValidString(input.ContentSource) || input.ContentSource == "" || len(input.ContentSource) > MaxSnippetBodyBytes {
		fields["content_source"] = "片段正文不能为空或超过 128 KiB"
	}
	if input.Visibility != SnippetPrivate && input.Visibility != SnippetShared {
		fields["visibility"] = "保存时请选择私密或共享；撤回使用独立操作"
	}
	// Dynamic resources/widgets depend on article identity. Reject them instead
	// of silently borrowing the containing article's permissions or private data.
	tokens := html.NewTokenizer(strings.NewReader(input.ContentSource))
	for kind := tokens.Next(); kind != html.ErrorToken; kind = tokens.Next() {
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		for _, a := range tokens.Token().Attr {
			if strings.HasPrefix(a.Key, "data-np-") || strings.HasPrefix(a.Key, "data-npr-") || strings.HasPrefix(a.Key, "data-npw-") {
				fields["content_source"] = "共享片段支持静态富文本，不能嵌套片段、资源或交互组件"
			}
		}
	}
	if len(fields) > 0 {
		return input, "", &ValidationError{Fields: fields}
	}
	body := contenthtml.Sanitize(input.ContentSource)
	if strings.TrimSpace(body) == "" || len(body) > MaxSnippetBodyBytes {
		return input, "", &ValidationError{Fields: map[string]string{"content_source": "净化后的片段正文为空或过长"}}
	}
	return input, body, nil
}

func CheckSnippetAccess(a SnippetAccess, write bool) error {
	if !a.Read || a.UserID < 0 || a.Identity == "" || len(a.Identity) > 160 || !utf8.ValidString(a.Identity) || containsControl(a.Identity) || ValidateRevisionActor(a.Actor) != nil {
		return ErrSnippetPermission
	}
	if write && !a.Manage && !(a.WriteOwn && a.UserID > 0) {
		return ErrSnippetPermission
	}
	return nil
}

func SnippetReadable(s Snippet, a SnippetAccess) bool {
	return a.Manage || (a.Read && s.Visibility == SnippetShared)
}
func SnippetVersionShared(s Snippet, version int64) bool {
	return s.Visibility == SnippetShared && s.SharedSince > 0 && version >= s.SharedSince && version <= s.CurrentVersion
}
func SnippetPostWritable(p Post, a SnippetAccess) bool {
	return a.Manage || a.WriteOwn && a.UserID > 0 && p.AuthorID == a.UserID
}

func NextSnippetRecord(current SnippetRecord, id int64, m Mutation, input SnippetInput, a SnippetAccess, now time.Time) (SnippetRecord, error) {
	if CheckSnippetAccess(a, true) != nil || !a.Manage {
		return SnippetRecord{}, ErrSnippetPermission
	}
	if current.Version != m.ExpectedVersion || current.Version == math.MaxInt64 {
		return SnippetRecord{}, ErrVersionConflict
	}
	input, body, err := NormalizeSnippetInput(input)
	if err != nil {
		return SnippetRecord{}, err
	}
	if current.ID == 0 {
		current.Snippet = Snippet{ID: id, CreatedAt: now}
	}
	current.Revisions = slices.Clone(current.Revisions)
	current.Version++
	current.CurrentVersion = current.Version
	if input.Visibility == SnippetShared {
		if current.Visibility != SnippetShared {
			current.SharedSince = current.Version
		}
	} else {
		current.SharedSince = 0
	}
	current.Visibility, current.Title, current.UpdatedAt = input.Visibility, input.Title, now
	current.Revisions = append(current.Revisions, SnippetRevision{Version: current.CurrentVersion, Title: input.Title, ContentSource: input.ContentSource, ContentHTML: body, Actor: a.Actor, CreatedAt: now})
	return current, nil
}

func WithdrawSnippetRecord(current SnippetRecord, m Mutation, a SnippetAccess, now time.Time) (SnippetRecord, error) {
	if CheckSnippetAccess(a, true) != nil || !a.Manage {
		return SnippetRecord{}, ErrSnippetPermission
	}
	if current.Version != m.ExpectedVersion || current.Version == math.MaxInt64 {
		return SnippetRecord{}, ErrVersionConflict
	}
	if current.Visibility == SnippetWithdrawn {
		return current, nil
	}
	current.Version++
	current.Visibility, current.SharedSince, current.UpdatedAt = SnippetWithdrawn, 0, now
	return current, nil
}

func SnippetRecordForAccess(record SnippetRecord, a SnippetAccess) SnippetRecord {
	revisions := []SnippetRevision{}
	for _, rev := range record.Revisions {
		if a.Manage || SnippetVersionShared(record.Snippet, rev.Version) {
			revisions = append(revisions, rev)
		}
	}
	record.Revisions = revisions
	return record
}

func SnippetPostImpact(p Post, id, target int64) SnippetImpact {
	i := SnippetImpact{ID: p.ID, Version: p.Version, Title: p.Title, Status: p.Status, Versions: []int64{}}
	for _, ref := range contenthtml.SnippetReferences(p.ContentSource) {
		if ref.ID != id || ref.Version < 1 {
			continue
		}
		i.References++
		if ref.Version != target {
			i.Changes++
		}
		if !slices.Contains(i.Versions, ref.Version) {
			i.Versions = append(i.Versions, ref.Version)
		}
	}
	slices.Sort(i.Versions)
	return i
}

func NormalizeSnippetUpdate(u SnippetUpdate, apply bool) (SnippetUpdate, error) {
	if u.SnippetID <= 0 || u.SnippetVersion <= 0 || len(u.Items) < 1 || len(u.Items) > MaxBulkMutations {
		return u, &ValidationError{Fields: map[string]string{"items": "请选择 1 至 100 篇文章及其准确版本"}}
	}
	u.Items = slices.Clone(u.Items)
	slices.SortFunc(u.Items, func(a, b SnippetSelection) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for i, v := range u.Items {
		if v.ID <= 0 || v.Version <= 0 || i > 0 && u.Items[i-1].ID == v.ID {
			return u, &ValidationError{Fields: map[string]string{"items": "文章编号与版本必须为正整数且不能重复"}}
		}
	}
	if apply && (!validSubmissionDigest(u.Digest) || !validSubmissionDigest(u.RequestKey)) {
		return u, &ValidationError{Fields: map[string]string{"request_key": "提交需要预览摘要和独立的 64 位十六进制请求键"}}
	}
	return u, nil
}

func SnippetUpdateFingerprint(u SnippetUpdate, a SnippetAccess) string {
	u.RequestKey = ""
	raw, _ := json.Marshal(struct {
		Identity string
		Update   SnippetUpdate
	}{a.Identity, u})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// BuildSnippetPlan checks the exact selected set. New, unselected references
// are deliberately not included in a previously reviewed operation.
func BuildSnippetPlan(s Snippet, posts []Post, u SnippetUpdate, a SnippetAccess) (SnippetPlan, error) {
	if err := CheckSnippetAccess(a, true); err != nil {
		return SnippetPlan{}, err
	}
	if !SnippetReadable(s, a) {
		return SnippetPlan{}, ErrNotFound
	}
	if s.Version != u.SnippetVersion || s.Visibility != SnippetShared {
		return SnippetPlan{}, ErrVersionConflict
	}
	if len(posts) != len(u.Items) {
		return SnippetPlan{}, ErrNotFound
	}
	plan := SnippetPlan{Snippet: s, Items: []SnippetImpact{}}
	for i, p := range posts {
		if p.ID != u.Items[i].ID || !SnippetPostWritable(p, a) {
			return SnippetPlan{}, ErrSnippetPermission
		}
		if p.Version != u.Items[i].Version || p.Version == math.MaxInt64 {
			return SnippetPlan{}, ErrVersionConflict
		}
		impact := SnippetPostImpact(p, s.ID, s.CurrentVersion)
		if impact.References == 0 || impact.Changes == 0 {
			return SnippetPlan{}, ErrVersionConflict
		}
		updated, _ := contenthtml.UpdateSnippetReferences(p.ContentSource, s.ID, s.CurrentVersion)
		if len(updated) > MaxContentBytes {
			return SnippetPlan{}, ErrSnippetCapacity
		}
		plan.Items = append(plan.Items, impact)
	}
	raw, _ := json.Marshal(struct {
		Identity string
		Plan     SnippetPlan
	}{a.Identity, plan})
	digest := sha256.Sum256(raw)
	plan.Digest = hex.EncodeToString(digest[:])
	return plan, nil
}

func NewSnippetReceipt(u SnippetUpdate, plan SnippetPlan, a SnippetAccess, now time.Time) SnippetReceipt {
	r := SnippetReceipt{Identity: a.Identity, RequestKey: u.RequestKey, Fingerprint: SnippetUpdateFingerprint(u, a), SnippetID: u.SnippetID, SnippetVersion: u.SnippetVersion, TargetVersion: plan.Snippet.CurrentVersion, CreatedAt: now, Items: []SnippetSelection{}}
	for _, i := range plan.Items {
		r.Items = append(r.Items, SnippetSelection{i.ID, i.Version + 1})
	}
	return r
}

func ValidateSnippetRecord(s SnippetRecord) error {
	validation, err := NewSnippetHistoryValidator(s.Snippet)
	if err != nil || len(s.Revisions) == 0 || len(s.Revisions) > MaxSnippetRevisions {
		return ErrSnippetCorrupt
	}
	for _, rev := range s.Revisions {
		if err := validation.Add(rev); err != nil {
			return err
		}
	}
	return validation.Complete()
}

func ValidateSnippetReceipt(r SnippetReceipt) error {
	if r.Identity == "" || len(r.Identity) > 160 || !utf8.ValidString(r.Identity) || containsControl(r.Identity) || !validSubmissionDigest(r.RequestKey) || !validSubmissionDigest(r.Fingerprint) || r.SnippetID <= 0 || r.TargetVersion <= 0 || r.SnippetVersion < r.TargetVersion || r.CreatedAt.IsZero() || len(r.Items) == 0 || len(r.Items) > MaxBulkMutations {
		return ErrSnippetCorrupt
	}
	for i, item := range r.Items {
		if item.ID <= 0 || item.Version < 2 || i > 0 && r.Items[i-1].ID >= item.ID {
			return ErrSnippetCorrupt
		}
	}
	return nil
}

func ValidateSnippetState(records []SnippetRecord, receipts []SnippetReceipt, nextID int64) error {
	_, err := validateSnippetState(records, receipts, nextID)
	return err
}

// Return the receipt bytes already counted during full validation. File loads
// keep this derived total in memory; the document never supplies a trusted sum.
func validateSnippetState(records []SnippetRecord, receipts []SnippetReceipt, nextID int64) (int, error) {
	if len(records) > MaxSnippets || len(receipts) > MaxSnippetReceipts || nextID < 1 || nextID == math.MaxInt64 {
		return 0, ErrSnippetCorrupt
	}
	ids, keys := map[int64]bool{}, map[string]bool{}
	bytes, revisions, receiptBytes := 0, 0, 0
	for _, s := range records {
		if ids[s.ID] || s.ID >= nextID || ValidateSnippetRecord(s) != nil {
			return 0, ErrSnippetCorrupt
		}
		ids[s.ID] = true
		bytes += SnippetStorageSize(s)
		revisions += len(s.Revisions)
	}
	for _, r := range receipts {
		key := fmt.Sprintf("%s\x00%s", r.Identity, r.RequestKey)
		if keys[key] || !ids[r.SnippetID] || ValidateSnippetReceipt(r) != nil {
			return 0, ErrSnippetCorrupt
		}
		keys[key] = true
		receiptBytes += SnippetReceiptSize(r)
	}
	if bytes > MaxSnippetStorageBytes || revisions > MaxSnippetRevisions || receiptBytes > MaxSnippetReceiptBytes {
		return 0, ErrSnippetCorrupt
	}
	return receiptBytes, nil
}
