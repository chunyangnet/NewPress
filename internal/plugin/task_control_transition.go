package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"newpress.local/newpress/internal/packagefs"
)

const taskTransitionLimit = 3*MaxTaskControlBytes + 4096

type taskControlTransition struct {
	Schema  int         `json:"schema"`
	Action  string      `json:"action"`
	Before  TaskControl `json:"before"`
	Blocked TaskControl `json:"blocked"`
	Next    TaskControl `json:"next"`
}

func sameTaskControl(a, b TaskControl) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && string(x) == string(y)
}

func validateTaskTransition(t taskControlTransition) error {
	if t.Schema != 1 || (t.Action != "update" && t.Action != "rollback" && t.Action != "uninstall") ||
		ValidateTaskControl(t.Before) != nil || ValidateTaskControl(t.Blocked) != nil || ValidateTaskControl(t.Next) != nil ||
		t.Before.TransitionID != "" || t.Blocked.TransitionID == "" || t.Next.TransitionID != "" ||
		t.Before.Slug != t.Blocked.Slug || t.Before.Slug != t.Next.Slug ||
		t.Blocked.Revision != t.Before.Revision+1 || t.Next.Revision != t.Blocked.Revision+1 ||
		t.Blocked.Enabled || t.Next.Enabled || t.Blocked.Epoch.ActorID != 0 || t.Next.Epoch.ActorID != 0 ||
		t.Blocked.Epoch.Token == t.Before.Epoch.Token || t.Next.Epoch.Token == t.Before.Epoch.Token {
		return ErrTaskControlCorrupt
	}
	expected := t.Before
	expected.Revision, expected.Enabled, expected.Epoch, expected.TransitionID = t.Blocked.Revision, false, t.Blocked.Epoch, t.Blocked.TransitionID
	expected.TransitionBytes = t.Blocked.TransitionBytes
	if !sameTaskControl(expected, t.Blocked) {
		return ErrTaskControlCorrupt
	}
	if t.Action == "uninstall" && !t.Next.Retired || t.Action != "uninstall" && t.Next.Retired != t.Before.Retired {
		return ErrTaskControlCorrupt
	}
	budget, err := TaskControlBudgetBytes(t.Next)
	if err != nil || budget != t.Blocked.TransitionBytes {
		return ErrTaskControlCorrupt
	}
	return nil
}

func (m *Manager) taskTransitionPath(slug string) string {
	return filepath.Join(m.root, ".task-control-transitions", slug+".json")
}

// A private intent is written before the shared blocking CAS. Once blocked,
// other instances cannot save or enable until this owner completes/reconciles
// the local package switch. Interrupted work always recovers disabled.
func (m *Manager) beginTaskControlTransition(ctx context.Context, before, desired TaskControl, action string) (*taskControlTransition, error) {
	if before.TransitionID != "" || desired.Slug != before.Slug {
		return nil, ErrTaskControlConflict
	}
	blocked, err := NextTaskControl(before)
	if err != nil {
		return nil, err
	}
	blocked.Epoch, err = freshTaskEpoch(0)
	if err != nil {
		return nil, err
	}
	blocked.Enabled, blocked.TransitionID = false, blocked.Epoch.Token
	desired.Revision = blocked.Revision
	desired, err = NextTaskControl(desired)
	if err != nil {
		return nil, err
	}
	desired.Enabled, desired.TransitionID = false, ""
	desired.Epoch, err = freshTaskEpoch(0)
	if err != nil {
		return nil, err
	}
	blocked.TransitionBytes, err = TaskControlBudgetBytes(desired)
	if err != nil {
		return nil, err
	}
	transition := &taskControlTransition{Schema: 1, Action: action, Before: before, Blocked: blocked, Next: desired}
	if err = validateTaskTransition(*transition); err != nil {
		return nil, err
	}
	path := m.taskTransitionPath(before.Slug)
	if err = packagefs.MkdirAllNoSymlink(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if _, err = packagefs.LstatNoSymlink(path); !os.IsNotExist(err) {
		return nil, errors.New("插件共享授权操作待恢复，请先重启并核对")
	}
	if err = WritePrivate(path, transition); err != nil {
		return nil, err
	}
	if err = m.taskControls.SaveTaskControl(ctx, before.Revision, blocked); err != nil {
		// A lost commit response is ambiguous. Keep the intent for a later
		// verified read; do not remove it merely because Save returned an error.
		return nil, err
	}
	return transition, nil
}

func (m *Manager) finishTaskControlTransition(ctx context.Context, transition *taskControlTransition) error {
	if transition == nil {
		return nil
	}
	current, found, err := m.loadTaskControl(ctx, transition.Before.Slug)
	if err != nil {
		return err
	}
	if !found || !sameTaskControl(current, transition.Blocked) {
		return ErrTaskControlConflict
	}
	if err = m.taskControls.SaveTaskControl(ctx, current.Revision, transition.Next); err != nil {
		return err
	}
	return os.Remove(m.taskTransitionPath(current.Slug))
}

func (m *Manager) beginTaskControlRetirement(ctx context.Context, slug string) (*taskControlTransition, error) {
	if m.taskControls == nil {
		return nil, nil
	}
	before, found, err := m.loadTaskControl(ctx, slug)
	if err != nil || !found || before.Retired && before.Configuration.Version == 0 && before.TransitionID == "" {
		return nil, err
	}
	desired := before
	desired.Retired = true
	desired.Configuration = defaultConfiguration(before.Manifest)
	return m.beginTaskControlTransition(ctx, before, desired, "uninstall")
}

func (m *Manager) recoverTaskControlTransitions() error {
	dir := filepath.Join(m.root, ".task-control-transitions")
	info, err := packagefs.LstatNoSymlink(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return ErrTaskControlCorrupt
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > MaxTaskControls {
		return ErrTaskControlCorrupt
	}
	if len(entries) > 0 && m.taskControls == nil {
		return errors.New("待恢复的插件共享授权需要原共享数据库")
	}
	for _, entry := range entries {
		slug := strings.TrimSuffix(entry.Name(), ".json")
		if entry.IsDir() || entry.Name() != slug+".json" || packagefs.ValidateSlug(slug) != nil {
			return ErrTaskControlCorrupt
		}
		path := m.taskTransitionPath(slug)
		t, err := ReadPrivate[taskControlTransition](path, taskTransitionLimit)
		if err != nil || validateTaskTransition(t) != nil || t.Before.Slug != slug {
			return ErrTaskControlCorrupt
		}
		ctx, cancel := taskControlContext()
		err = m.recoverTaskControlTransition(ctx, t)
		cancel()
		if err != nil {
			return err
		}
		if err = os.Remove(path); err != nil {
			return err
		}
		m.nextGeneration(slug)
	}
	return nil
}

func (m *Manager) recoverTaskControlTransition(ctx context.Context, t taskControlTransition) error {
	current, found, err := m.loadTaskControl(ctx, t.Before.Slug)
	if err != nil {
		return err
	}
	if !found {
		return ErrTaskControlConflict
	}
	if current.TransitionID == "" {
		// Either the initial blocking CAS lost a race (no local switch was
		// started), or the final CAS committed before cleanup was interrupted.
		// Neither case authorizes replaying this private intent.
		if current.Revision < t.Before.Revision || current.Revision == t.Before.Revision && !sameTaskControl(current, t.Before) {
			return ErrTaskControlConflict
		}
		return nil
	}
	if !sameTaskControl(current, t.Blocked) {
		return ErrTaskControlConflict
	}
	next := t.Next
	localPresent := false
	directory := filepath.Join(m.root, t.Before.Slug)
	info, err := packagefs.LstatNoSymlink(directory)
	if t.Action == "uninstall" {
		if err == nil && info.IsDir() {
			localPresent = true
			// Detachment did not commit. Keep the original authority disabled.
			next = t.Before
			next.Revision, next.Enabled, next.Epoch = t.Next.Revision, false, t.Next.Epoch
		} else if !os.IsNotExist(err) {
			return ErrTaskControlCorrupt
		}
	} else {
		if err != nil || !info.IsDir() {
			return ErrTaskControlCorrupt
		}
		localPresent = true
		manifest, err := readManifest(filepath.Join(directory, "plugin.json"))
		if err != nil {
			return ErrTaskControlCorrupt
		}
		module, _, err := readModule(filepath.Join(directory, filepath.FromSlash(manifest.Entry)))
		if err != nil || verifyIntegrity(directory, manifest, module) != nil {
			return ErrTaskControlCorrupt
		}
		if taskControlPackageMatches(t.Before, manifest, module) {
			next = t.Before
			next.Revision, next.Enabled, next.Epoch = t.Next.Revision, false, t.Next.Epoch
		} else if !taskControlPackageMatches(t.Next, manifest, module) {
			return ErrTaskControlConflict
		}
	}
	if localPresent {
		if err := writeMarker(filepath.Join(directory, ".disabled"), []byte("recovered disabled; administrator must review and enable\n")); err != nil {
			return err
		}
	}
	return m.taskControls.SaveTaskControl(ctx, current.Revision, next)
}
