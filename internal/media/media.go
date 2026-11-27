package media

import (
	"errors"
	"fmt"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

const (
	documentSchema     = 3
	documentKind       = "newpress/media-library"
	maxDocumentBytes   = 8 << 20
	defaultUploadLimit = 64 << 20
	maxImageDimension  = 32_768
	maxImagePixels     = 100_000_000
	maxFilenameRunes   = 140
	maxTitleRunes      = 160
	maxAltTextRunes    = 250
	maxCaptionRunes    = 2_000
	// Image processing allocates several decoded and resized buffers. Keep a
	// small default gate to bound simultaneous work.
	defaultImageConcurrency = 2

	KindImage    = "image"
	KindDocument = "document"
	KindAudio    = "audio"
	KindVideo    = "video"

	StatusActive = "active"
	StatusTrash  = "trash"
	StatusAll    = "all"
)

var (
	ErrNotFound        = errors.New("media item not found")
	ErrDuplicate       = errors.New("media object already exists")
	ErrTooLarge        = errors.New("media upload is too large")
	ErrUnsupportedType = errors.New("media type is not supported")
	ErrInvalidInput    = errors.New("media input is invalid")
	ErrVersionConflict = errors.New("media version conflict")
	ErrObjectMissing   = errors.New("media object is missing")
	ErrTrashed         = errors.New("media item is in trash")
	ErrNotTrashed      = errors.New("media item must be in trash")
	ErrDuplicateTrash  = errors.New("duplicate media item is in trash")
	ErrObjectCorrupt   = errors.New("media object checksum does not match")
)

type Item struct {
	ID             int64     `json:"id"`
	Version        int64     `json:"version"`
	Storage        string    `json:"storage"`
	ObjectKey      string    `json:"object_key"`
	PublicURL      string    `json:"public_url"`
	Filename       string    `json:"filename"`
	Title          string    `json:"title"`
	AltText        string    `json:"alt_text,omitempty"`
	Caption        string    `json:"caption,omitempty"`
	MIMEType       string    `json:"mime_type"`
	Kind           string    `json:"kind"`
	SizeBytes      int64     `json:"size_bytes"`
	Width          int       `json:"width,omitempty"`
	Height         int       `json:"height,omitempty"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	UploadedBy     string    `json:"uploaded_by,omitempty"`
	UploadSource   string    `json:"upload_source"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	TrashedAt      time.Time `json:"trashed_at,omitzero"`
	Image          ImageSet  `json:"image,omitzero"`
}

func (item Item) Status() string {
	if !item.TrashedAt.IsZero() {
		return StatusTrash
	}
	return StatusActive
}

type UploadInput struct {
	UploadSource string
	Filename     string
	Title        string
	AltText      string
	Caption      string
	UploadedBy   string
}

type UpdateInput struct {
	Title   string
	AltText string
	Caption string
}

type Query struct {
	Status string
	Search string
	Kind   string
	Limit  int
	Offset int
}

type List struct {
	Items        []Item
	Total        int
	TotalBytes   int64
	KindCounts   map[string]int
	StatusCounts map[string]int
}

type Snapshot struct {
	Count       int
	TotalBytes  int64
	KindCounts  map[string]int
	Revision    string
	UpdatedAt   time.Time
	ActiveCount int
	TrashCount  int
}

type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

type document struct {
	Schema    int       `json:"schema"`
	Kind      string    `json:"kind"`
	Revision  string    `json:"revision"`
	NextID    int64     `json:"next_id"`
	UpdatedAt time.Time `json:"updated_at"`
	Items     []Item    `json:"items"`
}

type Manager struct {
	mu            sync.RWMutex
	processing    imageProcessingLimiter
	root          string
	indexPath     string
	items         map[int64]Item
	checksumIndex map[string]int64
	memoryObjects map[string][]byte
	nextID        int64
	updatedAt     time.Time
}

// ManagerOptions controls resource limits for CPU-heavy media operations.
// ImageConcurrency is deliberately bounded; zero selects the safe default.
// Concurrent images also share the source pixel and byte processing budgets.
type ManagerOptions struct {
	ImageConcurrency int
}

func DefaultManagerOptions() ManagerOptions {
	return ManagerOptions{ImageConcurrency: defaultImageConcurrency}
}

func NewManager(root string) (*Manager, error) {
	return NewManagerWithOptions(root, DefaultManagerOptions())
}

func NewManagerWithOptions(root string, options ManagerOptions) (*Manager, error) {
	if options.ImageConcurrency == 0 {
		options.ImageConcurrency = defaultImageConcurrency
	}
	if options.ImageConcurrency < 1 || options.ImageConcurrency > 16 {
		return nil, fmt.Errorf("image concurrency must be between 1 and 16")
	}
	manager := &Manager{
		root:          root,
		items:         make(map[int64]Item),
		checksumIndex: make(map[string]int64),
		memoryObjects: make(map[string][]byte),
		processing:    imageProcessingLimiter{limit: options.ImageConcurrency, active: make(map[int64]struct{})},
		nextID:        1,
	}
	if root == "" {
		return manager, nil
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve media directory: %w", err)
	}
	manager.root = absolute
	manager.indexPath = filepath.Join(absolute, "library.json")
	for _, directory := range []string{absolute, filepath.Join(absolute, "objects"), filepath.Join(absolute, "tmp")} {
		if err := packagefs.MkdirAllNoSymlink(directory, 0o750); err != nil {
			return nil, fmt.Errorf("create media directory %q: %w", directory, err)
		}
	}
	stored, err := loadDocument(manager.indexPath)
	if errors.Is(err, os.ErrNotExist) {
		return manager, nil
	}
	if err != nil {
		return nil, err
	}
	if err := manager.loadStored(stored); err != nil {
		return nil, err
	}
	if err := manager.cleanupDerivedLocked(0); err != nil {
		return nil, fmt.Errorf("clean orphaned image derivatives: %w", err)
	}
	return manager, nil
}

func (m *Manager) Ready() error {
	if m == nil {
		return errors.New("media manager is nil")
	}
	if m.root == "" {
		return nil
	}
	for _, directory := range []string{m.root, filepath.Join(m.root, "objects"), filepath.Join(m.root, "tmp")} {
		info, err := packagefs.LstatNoSymlink(directory)
		if err != nil || !info.IsDir() {
			if err == nil {
				err = errors.New("path is not a directory")
			}
			return fmt.Errorf("media directory %q is not ready: %w", directory, err)
		}
	}
	return nil
}

func (m *Manager) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{KindCounts: emptyKindCounts()}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := m.sortedItemsLocked()
	counts, totalBytes := summarize(items)
	states := countStates(items)
	return Snapshot{
		Count: len(items), TotalBytes: totalBytes, KindCounts: counts,
		Revision: libraryRevision(m.nextID, items), UpdatedAt: m.updatedAt,
		ActiveCount: states[StatusActive], TrashCount: states[StatusTrash],
	}
}

func (m *Manager) List(query Query) (List, error) {
	if m == nil {
		return List{}, errors.New("media manager is nil")
	}
	normalized, err := normalizeQuery(query)
	if err != nil {
		return List{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := m.sortedItemsLocked()
	states := countStates(all)
	statusItems := make([]Item, 0, len(all))
	for _, item := range all {
		if normalized.Status == StatusAll || item.Status() == normalized.Status {
			statusItems = append(statusItems, item)
		}
	}
	counts, totalBytes := summarize(statusItems)
	needle := strings.ToLower(normalized.Search)
	filtered := make([]Item, 0, len(all))
	for _, item := range statusItems {
		if normalized.Kind != "" && item.Kind != normalized.Kind {
			continue
		}
		if needle != "" {
			haystack := strings.ToLower(strings.Join([]string{
				item.Filename, item.Title, item.AltText, item.Caption, item.UploadedBy, item.MIMEType,
			}, " "))
			if !strings.Contains(haystack, needle) {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	result := List{Total: len(filtered), TotalBytes: totalBytes, KindCounts: counts, StatusCounts: states}
	if normalized.Offset >= len(filtered) {
		result.Items = []Item{}
		return result, nil
	}
	end := min(len(filtered), normalized.Offset+normalized.Limit)
	result.Items = append([]Item(nil), filtered[normalized.Offset:end]...)
	return result, nil
}

func (m *Manager) Get(id int64) (Item, error) {
	if m == nil || id <= 0 {
		return Item{}, ErrNotFound
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.items[id]
	if !ok {
		return Item{}, ErrNotFound
	}
	return item, nil
}

func validKind(kind string) bool {
	switch kind {
	case KindImage, KindDocument, KindAudio, KindVideo:
		return true
	default:
		return false
	}
}

func summarize(items []Item) (map[string]int, int64) {
	counts := emptyKindCounts()
	var totalBytes int64
	for _, item := range items {
		counts[item.Kind]++
		totalBytes += item.SizeBytes
		for _, variant := range item.Image.Variants {
			totalBytes += variant.Native.SizeBytes + variant.WebP.SizeBytes
		}
	}
	return counts, totalBytes
}

func emptyKindCounts() map[string]int {
	return map[string]int{KindImage: 0, KindDocument: 0, KindAudio: 0, KindVideo: 0}
}

func storageName(root string) string {
	if root == "" {
		return "memory"
	}
	return "local"
}
