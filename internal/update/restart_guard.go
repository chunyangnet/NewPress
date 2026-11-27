package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"newpress.local/newpress/internal/config"
)

// Retain one post-commit recovery opportunity, bound to this exact pair of
// releases. Older outages and intentional supervisor shutdowns require no switch.
type restartGuard struct {
	Candidate Release   `json:"candidate"`
	Previous  Release   `json:"previous"`
	Until     time.Time `json:"until"`
	State     State     `json:"state"`
}

func startReady(ctx context.Context, root, seed string, cfg config.Config, release Release) (*childProcess, error) {
	c, err := startChild(root, seed, cfg, release)
	if err != nil {
		return nil, err
	}
	if err = awaitReady(ctx, c, cfg.Address, 60*time.Second); err != nil {
		c.abort(15 * time.Second)
		return nil, err
	}
	return c, nil
}
func recoverRecentExit(ctx context.Context, m *Manager, seed string, cfg config.Config, active Release, at time.Time) (*childProcess, Release, error) {
	if active.ID == "" || ctx.Err() != nil {
		return nil, active, nil
	}
	var guard restartGuard
	err := readJSON(filepath.Join(m.root, "guard.json"), &guard)
	if errors.Is(err, os.ErrNotExist) {
		return nil, active, nil
	}
	if err != nil {
		return nil, active, err
	}
	if guard.Candidate != active || guard.Previous.ID == active.ID || !guard.Until.After(at) {
		return nil, active, nil
	}
	tx := Transaction{Generation: LauncherGeneration, Candidate: active, Previous: guard.Previous, State: guard.State}
	// Once the journal is durable every subsequent restart selects Previous,
	// even if power is lost before the guard is consumed or the rollback starts.
	if err = writeJSON(filepath.Join(m.root, "transaction.json"), tx); err != nil {
		return nil, active, err
	}
	old, err := restorePrevious(ctx, m, seed, cfg, tx, "新版本在重启保护期内退出，正在恢复上一可用版本")
	return old, tx.Previous, err
}

// Keep the transaction until the previous release is serving and the result is durable.
func restorePrevious(ctx context.Context, m *Manager, seed string, cfg config.Config, tx Transaction, reason string) (*childProcess, error) {
	tx.State.Previous = tx.Previous.Version
	tx.State.ProtectedUntil = time.Time{}
	tx.State.step("rollback", 98, reason)
	if err := m.save(tx.State); err != nil {
		return nil, err
	}
	for _, name := range []string{"guard.json", "request.json"} {
		if err := removeRecord(m.root, name); err != nil {
			return nil, err
		}
	}
	cfg.AutoMigrate = false
	old, err := startReady(ctx, m.root, seed, cfg, tx.Previous)
	if err != nil {
		return nil, err
	}
	tx.State.step("rolled_back", 100, "上一可用版本已恢复并通过健康检查")
	if err = writeJSON(filepath.Join(m.root, "active.json"), tx.Previous); err == nil {
		err = m.save(tx.State)
	}
	if err == nil {
		err = removeRecord(m.root, "transaction.json")
	}
	if err != nil {
		old.stop()
		return nil, err
	}
	return old, nil
}
