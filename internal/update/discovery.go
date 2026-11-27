package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Discovery has its own file so scheduled checks never replace an installation receipt.
type Discovery struct {
	Checked  time.Time `json:"checked"`
	Message  string    `json:"message"`
	Interval string    `json:"interval,omitempty"`
}

const defaultCheckInterval = 6 * time.Hour

func checkInterval() (time.Duration, error) {
	raw := os.Getenv("NEWPRESS_UPDATE_CHECK_INTERVAL")
	if raw == "" {
		return defaultCheckInterval, nil
	}
	d, err := time.ParseDuration(raw)
	if err == nil && d == 0 {
		return 0, nil
	}
	if err != nil || d < time.Minute || d > 7*24*time.Hour {
		return 0, errors.New("NEWPRESS_UPDATE_CHECK_INTERVAL 必须为 0 或介于 1m 和 168h 之间")
	}
	return d, nil
}
func (m *Manager) discovery() Discovery {
	var result Discovery
	// A decoder can populate fields before reporting an unknown field or type
	// error. This disposable cache must never publish or schedule partial data.
	if err := readJSON(filepath.Join(m.root, "discovery.json"), &result); err != nil {
		return Discovery{}
	}
	return result
}
func (m *Manager) refreshCatalog(ctx context.Context, s Source) (Available, error) {
	if err := m.invalidate(); err != nil {
		return Available{}, err
	}
	a, err := m.discover(ctx, s)
	result := Discovery{Checked: time.Now().UTC()}
	if err != nil {
		result.Message = err.Error()
	} else {
		if newer(a.Manifest.Version, m.current) {
			result.Message = "可用版本 " + a.Manifest.Version
		} else {
			result.Message = "当前已是最新版本"
		}
		if err = writeJSON(filepath.Join(m.root, "available.json"), a); err != nil {
			return Available{}, errors.New("无法保存版本信息")
		}
	}
	if saveErr := writeJSON(filepath.Join(m.root, "discovery.json"), result); saveErr != nil {
		return Available{}, errors.New("无法保存检查结果")
	}
	return a, err
}
func (m *Manager) checkDue() bool {
	p, err := m.policy()
	if err != nil || !p.AutoCheck {
		return false
	}
	elapsed := time.Since(m.discovery().Checked)
	// A persisted wall-clock timestamp may be ahead after the clock moves back.
	// Refresh once instead of waiting for that timestamp plus another interval.
	return elapsed < 0 || elapsed >= p.interval()
}

func (m *Manager) scheduledCheck(ctx context.Context) {
	if !m.checkDue() {
		return
	}
	lock, err := m.lock()
	if err != nil {
		return
	}
	defer lock.Close()
	// A manual check or policy change may have won the lock since the first read.
	if !m.checkDue() {
		return
	}
	s, _, err := loadSource(m.root)
	if err != nil || s.validate() != nil {
		return
	}
	if _, err := publisherKey(s); err != nil {
		return
	}
	_, _ = m.refreshCatalog(ctx, s)
}
func (m *Manager) RunChecks(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.scheduledCheck(ctx)
		}
	}
}
