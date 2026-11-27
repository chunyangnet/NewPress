package plugin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

// TaskEpoch is private host state, never accepted from a plugin request. It is
// deliberately separate from settings version: settings.set can change values
// without giving a task a new authorization lifetime.
type TaskEpoch struct {
	Schema    int       `json:"schema"`
	Token     string    `json:"token"`
	StartedAt time.Time `json:"started_at"`
	ActorID   int64     `json:"actor_id,omitempty"`
}

func rotateTaskEpoch(dataDir string) error {
	if err := packagefs.MkdirAllNoSymlink(dataDir, 0700); err != nil {
		return err
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	return WritePrivate(filepath.Join(dataDir, "task-epoch.json"), TaskEpoch{Schema: 1, Token: hex.EncodeToString(token[:]), StartedAt: time.Now().UTC()})
}
func readTaskEpoch(dataDir string) (TaskEpoch, error) {
	e, err := ReadPrivate[TaskEpoch](filepath.Join(dataDir, "task-epoch.json"), 1024)
	if err != nil {
		return TaskEpoch{}, ErrInactive
	}
	b, err := hex.DecodeString(e.Token)
	if err != nil || len(b) != 32 || e.Token != hex.EncodeToString(b) || e.Schema != 1 || e.ActorID < 0 || e.StartedAt.IsZero() {
		return TaskEpoch{}, ErrInactive
	}
	return e, nil
}

// WithTaskEpoch serializes a short local side effect against lifecycle and
// configuration changes. Never perform network calls or reenter Manager in fn.
// The host must additionally validate the current principal and capability.
func (m *Manager) WithTaskEpoch(slug, token string, fn func() error) error {
	if token == "" || fn == nil {
		return ErrInactive
	}
	return m.WithData(slug, true, 0, func(dir string, _ uint64) error {
		if m.taskControls != nil {
			// A local callback cannot fence a shared business transaction.
			return ErrInactive
		}
		epoch, err := readTaskEpoch(dir)
		if err != nil || epoch.Token != token {
			return ErrInactive
		}
		return fn()
	})
}

// CurrentTaskEpoch returns an enabled installation's durable authorization
// lifetime. Installations without one cannot execute tasks.
// Callers still need hook/capability/integrity and current-principal checks.
func (m *Manager) CurrentTaskEpoch(slug string) (TaskEpoch, error) {
	var epoch TaskEpoch
	err := m.WithData(slug, true, 0, func(dir string, _ uint64) error {
		manifest, err := readManifest(filepath.Join(filepath.Dir(dir), "plugin.json"))
		if err != nil {
			return err
		}
		if manifest.Runtime != "wasm/core-v1" || len(manifest.Hooks) == 0 {
			return errors.New("plugin has no business runtime")
		}
		if m.taskControls != nil {
			ctx, cancel := taskControlContext()
			defer cancel()
			module, _, err := readModule(filepath.Join(filepath.Dir(dir), filepath.FromSlash(manifest.Entry)))
			if err != nil {
				return err
			}
			if err = verifyIntegrity(filepath.Dir(dir), manifest, module); err != nil {
				return ErrInactive
			}
			control, err := m.activeTaskControl(ctx, manifest, module)
			if err != nil {
				return err
			}
			epoch = control.Epoch
			return nil
		}
		epoch, err = readTaskEpoch(dir)
		return err
	})
	return epoch, err
}
