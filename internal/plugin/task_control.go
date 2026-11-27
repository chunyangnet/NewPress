package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

const (
	MaxTaskControlBytes = 144 << 10
	MaxTaskControls     = 10000
	MaxTaskControlTotal = 32 << 20
)

var (
	ErrTaskControlConflict    = errors.New("插件共享授权已变化，请刷新核对后重试")
	ErrTaskControlUnavailable = errors.New("插件共享授权暂不可用")
	ErrTaskControlCorrupt     = errors.New("插件共享授权记录不一致，请停机核对")
	ErrTaskControlCapacity    = errors.New("插件共享授权保留配额已满")
)

// TaskControl is private host authority. Package bytes remain local, but a
// participating instance must match both package digests before using grants.
// Epoch.ActorID records explicit event-subscription consent, not a guest actor.
type TaskControl struct {
	Schema          int           `json:"schema"`
	Slug            string        `json:"slug"`
	Revision        int64         `json:"revision"`
	Manifest        Manifest      `json:"manifest"`
	ModuleDigest    string        `json:"module_digest"`
	Configuration   Configuration `json:"configuration"`
	Epoch           TaskEpoch     `json:"epoch"`
	Enabled         bool          `json:"enabled"`
	Retired         bool          `json:"retired"`
	TransitionID    string        `json:"transition_id,omitempty"`
	TransitionBytes int           `json:"transition_bytes,omitempty"`
}

// TaskControlStore must atomically compare the previous revision, validate the
// stored row, and retain disabled/retired identities. Missing authority is never
// imported from local files by a read or a worker startup.
type TaskControlStore interface {
	LoadTaskControl(context.Context, string) (TaskControl, bool, error)
	ListTaskControls(context.Context) ([]TaskControl, error)
	SaveTaskControl(context.Context, int64, TaskControl) error
}

type ManagerOption func(*Manager)

func WithTaskControlStore(store TaskControlStore) ManagerOption {
	return func(m *Manager) { m.taskControls = store }
}

func freshTaskEpoch(actorID int64) (TaskEpoch, error) {
	if actorID < 0 {
		return TaskEpoch{}, ErrInactive
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return TaskEpoch{}, err
	}
	return TaskEpoch{Schema: 1, Token: hex.EncodeToString(token[:]), StartedAt: time.Now().UTC(), ActorID: actorID}, nil
}

// NewTaskEpoch creates a host-only lifetime for a shared storage transition.
// Possession of this value alone does not grant task execution.
func NewTaskEpoch(actorID int64) (TaskEpoch, error) { return freshTaskEpoch(actorID) }

func validTaskEpoch(e TaskEpoch) bool {
	b, err := hex.DecodeString(e.Token)
	return err == nil && len(b) == 32 && e.Token == hex.EncodeToString(b) &&
		e.Schema == 1 && e.ActorID >= 0 && !e.StartedAt.IsZero()
}

// ValidateTaskControl is shared by the SQL repository and manager. SQL index
// columns are additionally checked by the repository before any CAS or read.
func ValidateTaskControl(c TaskControl) error {
	if c.Schema != 1 || c.Revision < 1 || packagefs.ValidateSlug(c.Slug) != nil ||
		c.Manifest.Slug != c.Slug || c.Manifest.Runtime != "wasm/core-v1" ||
		len(c.Manifest.Hooks) == 0 || ValidateManifest(c.Manifest) != nil ||
		!validTaskEpoch(c.Epoch) || validateConfiguration(c.Manifest, c.Configuration) != nil {
		return ErrTaskControlCorrupt
	}
	b, err := hex.DecodeString(c.ModuleDigest)
	if err != nil || len(b) != 32 || hex.EncodeToString(b) != c.ModuleDigest {
		return ErrTaskControlCorrupt
	}
	if c.TransitionID != "" {
		id, err := hex.DecodeString(c.TransitionID)
		if err != nil || len(id) != 32 || hex.EncodeToString(id) != c.TransitionID || c.Enabled || c.Epoch.ActorID != 0 || c.TransitionBytes < 1 || c.TransitionBytes > MaxTaskControlBytes {
			return ErrTaskControlCorrupt
		}
	} else if c.TransitionBytes != 0 {
		return ErrTaskControlCorrupt
	}
	if c.Retired {
		if c.Enabled || c.Epoch.ActorID != 0 {
			return ErrTaskControlCorrupt
		}
	} else if c.Configuration.Version < 1 {
		return ErrTaskControlCorrupt
	}
	config, err := json.Marshal(c.Configuration)
	if err != nil || len(config) > businessBytes {
		return ErrTaskControlCorrupt
	}
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > MaxTaskControlBytes {
		return ErrTaskControlCorrupt
	}
	return nil
}

// TaskControlBudgetBytes reserves the longest state metadata, including a
// future package transition. Values/manifest still consume their actual bytes.
// A blocked transition additionally retains room for its chosen final record,
// so another plugin cannot consume that capacity during the local package swap.
func TaskControlBudgetBytes(c TaskControl) (int, error) {
	if err := ValidateTaskControl(c); err != nil {
		return 0, err
	}
	reserved := c.TransitionBytes
	c.Revision, c.Configuration.Version = math.MaxInt64, math.MaxInt64
	c.Epoch.StartedAt = time.Date(2000, 1, 1, 0, 0, 0, 123456789, time.UTC)
	c.Epoch.ActorID = math.MaxInt64
	c.Enabled, c.Retired = false, false
	c.TransitionID, c.TransitionBytes = c.Epoch.Token, MaxTaskControlBytes
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > MaxTaskControlBytes {
		return 0, ErrTaskControlCapacity
	}
	return max(len(raw), reserved), nil
}

func NextTaskControl(c TaskControl) (TaskControl, error) {
	if c.Revision < 0 || c.Revision == math.MaxInt64 {
		return TaskControl{}, ErrTaskControlConflict
	}
	c.Revision++
	return c, nil
}

func taskControlManifestMatches(control TaskControl, manifest Manifest) bool {
	a, err := json.Marshal(control.Manifest)
	if err != nil {
		return false
	}
	b, err := json.Marshal(manifest)
	return err == nil && digestBytes(a) == digestBytes(b)
}

func taskControlPackageMatches(control TaskControl, manifest Manifest, module []byte) bool {
	return taskControlManifestMatches(control, manifest) && control.ModuleDigest == digestBytes(module)
}
