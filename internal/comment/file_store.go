package comment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/packagefs"
)

const (
	fileSchema     = 3
	fileKind       = "newpress/comments"
	maxFileBytes   = 16 << 20
	defaultPubSize = 50
)

type fileDocument struct {
	Schema      int                 `json:"schema"`
	Kind        string              `json:"kind"`
	NextID      int64               `json:"next_id"`
	UpdatedAt   time.Time           `json:"updated_at"`
	Items       []Comment           `json:"items"`
	Revisions   []Revision          `json:"revisions,omitempty"`
	Activities  []activity.Event    `json:"activities,omitempty"`
	Submissions []SubmissionReceipt `json:"submissions,omitempty"`
}

type FileStore struct {
	mu          sync.RWMutex
	path        string
	audit       *audit.Ledger
	auditFault  error
	items       map[int64]Comment
	revisions   map[int64][]Revision
	nextID      int64
	updatedAt   time.Time
	activities  []activity.Event
	submissions map[string]SubmissionReceipt
}

func NewFileStore(path string, seedDemo bool, ledgers ...*audit.Ledger) (*FileStore, error) {
	var ledger *audit.Ledger
	if len(ledgers) > 0 {
		ledger = ledgers[0]
	}
	store := &FileStore{path: path, audit: ledger, items: make(map[int64]Comment), revisions: make(map[int64][]Revision), submissions: make(map[string]SubmissionReceipt), nextID: 1}
	if path == "" {
		if seedDemo {
			store.seedDemoLocked(time.Now().UTC())
		}
		return store, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve comment store path: %w", err)
	}
	store.path = absolute
	if err := packagefs.MkdirAllNoSymlink(filepath.Dir(absolute), 0o750); err != nil {
		return nil, fmt.Errorf("create comment store directory: %w", err)
	}
	document, err := loadFileDocument(absolute)
	if errors.Is(err, os.ErrNotExist) {
		if seedDemo {
			store.seedDemoLocked(time.Now().UTC())
			event := audit.Event{Action: "comment.seeded", ObjectType: "comment_registry", ObjectID: "all", Actor: "system", Metadata: map[string]any{
				"comment_count": len(store.items), "revision_count": 0,
			}}
			if err := store.persistLocked(store.items, store.revisions, store.nextID, store.updatedAt, &event); err != nil {
				return nil, err
			}
		}
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := validateFileDocument(document); err != nil {
		return nil, fmt.Errorf("stored comments are invalid: %w", err)
	}
	store.nextID = document.NextID
	store.activities = document.Activities
	store.updatedAt = document.UpdatedAt.UTC()
	for _, receipt := range document.Submissions {
		store.submissions[receipt.Key] = receipt
	}
	for _, item := range document.Items {
		store.items[item.ID] = item
	}
	for _, revision := range document.Revisions {
		store.revisions[revision.CommentID] = append(store.revisions[revision.CommentID], revision)
	}
	return store, nil
}

func (store *FileStore) Ready(context.Context) error {
	if store == nil {
		return errors.New("comment store is nil")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.path == "" {
		return nil
	}
	if store.auditFault != nil {
		return fmt.Errorf("comment store audit failure: %w", store.auditFault)
	}
	if store.audit != nil {
		if err := store.audit.Ready(); err != nil {
			return fmt.Errorf("comment store audit is not ready: %w", err)
		}
	}
	info, err := packagefs.LstatNoSymlink(filepath.Dir(store.path))
	if err != nil {
		return fmt.Errorf("inspect comment store directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("comment store parent is not a directory")
	}
	return nil
}

func (store *FileStore) ListApproved(_ context.Context, postID int64, limit, offset int) ([]Comment, int, error) {
	if store == nil || postID <= 0 || offset < 0 {
		return nil, 0, ErrInvalidInput
	}
	if limit <= 0 || limit > MaxListLimit {
		limit = defaultPubSize
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	items := make([]Comment, 0)
	for _, item := range store.items {
		if item.PostID == postID && item.Status == StatusApproved {
			items = append(items, item)
		}
	}
	SortPublic(items)
	total := len(items)
	if offset >= total {
		return []Comment{}, total, nil
	}
	end := min(total, offset+limit)
	return append([]Comment(nil), items[offset:end]...), total, nil
}

func (store *FileStore) ListAdmin(_ context.Context, query Query) (List, error) {
	if store == nil {
		return List{}, errors.New("comment store is nil")
	}
	normalized, err := NormalizeQuery(query)
	if err != nil {
		return List{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	all := store.itemsLocked()
	if normalized.UserID > 0 || normalized.OwnOnly {
		owned := make([]Comment, 0)
		for _, item := range all {
			if item.UserID == normalized.UserID && (!normalized.OwnOnly || item.Status == StatusPending || item.Status == StatusApproved) {
				owned = append(owned, item)
			}
		}
		all = owned
	}
	needle := strings.ToLower(normalized.Search)
	filtered := make([]Comment, 0, len(all))
	for _, item := range all {
		if normalized.PostID > 0 && item.PostID != normalized.PostID {
			continue
		}
		if normalized.Status != "" && item.Status != normalized.Status {
			continue
		}
		if needle != "" {
			haystack := strings.ToLower(strings.Join([]string{item.AuthorName, item.AuthorEmail, item.ContentSource}, " "))
			if normalized.OwnOnly {
				haystack = strings.ToLower(item.ContentSource)
			}
			if !strings.Contains(haystack, needle) {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	SortAdmin(filtered)
	result := List{Total: len(filtered), StatusCounts: StatusCounts(all), Items: []Comment{}}
	if normalized.Offset >= len(filtered) {
		return result, nil
	}
	end := min(len(filtered), normalized.Offset+normalized.Limit)
	result.Items = append(result.Items, filtered[normalized.Offset:end]...)
	return result, nil
}

func (store *FileStore) Get(_ context.Context, id int64) (Comment, error) {
	if store == nil || id <= 0 {
		return Comment{}, ErrNotFound
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	item, ok := store.items[id]
	if !ok {
		return Comment{}, ErrNotFound
	}
	return item, nil
}

func (store *FileStore) Snapshot(_ context.Context) (Snapshot, error) {
	if store == nil {
		return Snapshot{}, errors.New("comment store is nil")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	items := store.itemsLocked()
	return Snapshot{Total: len(items), StatusCounts: StatusCounts(items), UpdatedAt: store.updatedAt}, nil
}

func (store *FileStore) validateParentLocked(postID, parentID int64) error {
	if parentID == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, MaxThreadDepth)
	currentID := parentID
	for depth := 1; currentID > 0; depth++ {
		if depth >= MaxThreadDepth {
			return ErrInvalidParent
		}
		if _, exists := seen[currentID]; exists {
			return ErrInvalidParent
		}
		seen[currentID] = struct{}{}
		parent, ok := store.items[currentID]
		if !ok || parent.PostID != postID || parent.Status != StatusApproved {
			return ErrInvalidParent
		}
		currentID = parent.ParentID
	}
	return nil
}

func (store *FileStore) validateStaffReplyParentLocked(parent Comment) error {
	if parent.Status != StatusApproved && parent.Status != StatusPending {
		return ErrInvalidParent
	}
	seen := make(map[int64]struct{}, MaxThreadDepth)
	currentID := parent.ID
	for depth := 1; currentID > 0; depth++ {
		if depth >= MaxThreadDepth {
			return ErrInvalidParent
		}
		if _, exists := seen[currentID]; exists {
			return ErrInvalidParent
		}
		seen[currentID] = struct{}{}
		current, ok := store.items[currentID]
		if !ok || current.PostID != parent.PostID || (current.Status != StatusApproved && current.Status != StatusPending) {
			return ErrInvalidParent
		}
		currentID = current.ParentID
	}
	return nil
}

func (store *FileStore) itemsLocked() []Comment {
	items := make([]Comment, 0, len(store.items))
	for _, item := range store.items {
		items = append(items, item)
	}
	return items
}

func (store *FileStore) seedDemoLocked(now time.Time) {
	now = now.UTC().Truncate(time.Second)
	type fixture struct {
		postID, parentID  int64
		name, email, body string
		ago               time.Duration
	}
	fixtures := []fixture{
		{1, 0, "隧设计组", "chunnel@example.invalid", "内容、扩展能力和主题展示应该各守边界，这样系统才容易长期演进。", 2 * time.Hour},
		{1, 1, "WASM Lab", "wasm@example.invalid", "当插件只输出稳定数据结构时，主题可以大胆变化，权限边界也更清楚。", time.Hour},
		{2, 0, "独立创作者", "creator@example.invalid", "高密度不是堆叠元素，而是让读者更快分辨主次和下一步动作。", 26 * time.Hour},
		{2, 0, "Go 站长", "operator@example.invalid", "希望后续把评论审核、限速和备份边界也做成同样清晰的管理面。", 25 * time.Hour},
	}
	for _, fixture := range fixtures {
		created := now.Add(-fixture.ago)
		item := Comment{
			ID: store.nextID, PostID: fixture.postID, ParentID: fixture.parentID, Version: 1,
			AuthorName: fixture.name, AuthorEmail: fixture.email, Status: StatusApproved,
			ContentSource: fixture.body, ContentHTML: RenderPlainText(fixture.body), CreatedAt: created, UpdatedAt: created,
		}
		store.items[item.ID] = item
		store.nextID++
	}
	store.updatedAt = now
}
