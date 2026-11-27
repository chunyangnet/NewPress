package objectstorage

import (
	"context"
	"sort"
	"sync"
	"time"

	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/linkpage"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/plugin"
)

const maxRecords = 50000

var errRecordLimit = &OperationError{"同步记录已达 50000 条上限；已有任务继续处理，新对象暂不加入"}

type Record struct {
	ID          string    `json:"id"`
	ProfileID   string    `json:"profile_id"`
	Identity    string    `json:"identity"`
	ItemID      int64     `json:"item_id"`
	Filename    string    `json:"filename"`
	ObjectKey   string    `json:"object_key"`
	RemoteKey   string    `json:"remote_key"`
	SHA256      string    `json:"sha256"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	Status      string    `json:"status"`
	Operation   string    `json:"operation"`
	Attempts    int       `json:"attempts"`
	Next        time.Time `json:"next"`
	Updated     time.Time `json:"updated"`
	Verified    time.Time `json:"verified"`
	Error       string    `json:"error,omitempty"`
}

type Status struct {
	UploadEntries []media.UploadSource      `json:"upload_entries"`
	Settings      SettingsView              `json:"settings"`
	Enabled       bool                      `json:"enabled"`
	Running       bool                      `json:"running"`
	Namespace     string                    `json:"namespace"`
	Counts        map[string]int            `json:"counts"`
	ProfileCounts map[string]map[string]int `json:"profile_counts"`
	ProfileBytes  map[string]int64          `json:"profile_bytes"`
	TotalBytes    int64                     `json:"total_bytes"`
	SyncedBytes   int64                     `json:"synced_bytes"`
	Records       []Record                  `json:"records"`
	LastError     string                    `json:"last_error,omitempty"`
}

type ProbeResult struct {
	OK         bool     `json:"ok"`
	Stages     []string `json:"stages"`
	DurationMS int64    `json:"duration_ms"`
	CleanupOK  bool     `json:"cleanup_ok"`
}

type Service struct {
	// OnDeliveryChange is configured before Run and invoked only after a durable
	// successful upload/delete receipt. It receives public, unsigned URLs only.
	OnDeliveryChange func(string)
	manager          *plugin.Manager
	media            *media.Manager
	circles          community.Store
	circleFiles      *community.Files
	linkPages        linkpage.Store
	factory          BackendFactory
	mu               sync.Mutex // never acquire manager.WithData while holding this lock
	loaded           bool
	generation       uint64
	settings         Settings
	namespace        string
	records          map[string]Record
	backends         map[string]Backend
	active           context.Context
	configContext    context.Context
	configCancel     context.CancelFunc
	wake             chan struct{}
	lastError        string
	running          bool
	jobMu            sync.Mutex
	fileMu           sync.Mutex
	probeGate        chan struct{}
}

func New(m *plugin.Manager, library *media.Manager) *Service {
	return &Service{manager: m, media: library, factory: NewBackend, wake: make(chan struct{}, 1), probeGate: make(chan struct{}, 1)}
}

func (s *Service) Wake() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Status() (Status, error) {
	if err := s.load(false); err != nil {
		return Status{}, err
	}
	enabled := s.manager.WithData(plugin.ObjectStorage, true, 0, func(string, uint64) error { return nil }) == nil
	s.mu.Lock()
	defer s.mu.Unlock()
	v := Status{Settings: sanitized(s.settings), Enabled: enabled, Running: s.running, Namespace: s.namespace, Counts: map[string]int{}, ProfileCounts: map[string]map[string]int{}, Records: []Record{}, LastError: s.lastError}
	v.UploadEntries = UploadEntries()
	v.ProfileBytes = map[string]int64{}
	for _, r := range s.records {
		v.Counts[r.Status]++
		if v.ProfileCounts[r.ProfileID] == nil {
			v.ProfileCounts[r.ProfileID] = map[string]int{}
		}
		v.ProfileCounts[r.ProfileID][r.Status]++
		if r.Status != "removed" {
			v.TotalBytes += r.Size
		}
		if r.Status == "synced" {
			v.SyncedBytes += r.Size
			v.ProfileBytes[r.ProfileID] += r.Size
		}
		v.Records = append(v.Records, r)
	}
	sort.Slice(v.Records, func(i, j int) bool {
		if !v.Records[i].Updated.Equal(v.Records[j].Updated) {
			return v.Records[i].Updated.After(v.Records[j].Updated)
		}
		return v.Records[i].ID < v.Records[j].ID
	})
	if len(v.Records) > 100 {
		v.Records = v.Records[:100]
	}
	return v, nil
}

// PublicError is the redacted diagnostic shown by the HTTP API.
func PublicError(err error) string { return safeError(err) }
