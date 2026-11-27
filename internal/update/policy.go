package update

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"time"
)

var ErrPolicyConflict = errors.New("更新策略已被修改，请载入最新策略后再保存")

const policyProtocol = "1"

// Values from the deployment are used until an administrator saves a site policy.
type Policy struct {
	AutoCheck            bool    `json:"auto_check"`
	CheckIntervalMinutes float64 `json:"check_interval_minutes"`
	ObservationSeconds   float64 `json:"observation_seconds"`
}

type PolicySnapshot struct {
	Policy
	Customized            bool   `json:"customized"`
	Revision              string `json:"revision"`
	LauncherAppliesWindow bool   `json:"launcher_applies_window"`
}

func (p Policy) validate() error {
	if !(p.CheckIntervalMinutes >= 1 && p.CheckIntervalMinutes <= 7*24*60) {
		return errors.New("检查间隔应在 1 到 10080 分钟之间")
	}
	if !(p.ObservationSeconds >= 2 && p.ObservationSeconds <= 600) {
		return errors.New("健康观察时长应在 2 到 600 秒之间")
	}
	return nil
}

func (p Policy) interval() time.Duration {
	if !p.AutoCheck {
		return 0
	}
	return time.Duration(math.Round(p.CheckIntervalMinutes * float64(time.Minute)))
}

func (p Policy) window() time.Duration {
	return time.Duration(math.Round(p.ObservationSeconds * float64(time.Second)))
}

func deploymentPolicy() (Policy, error) {
	interval, err := checkInterval()
	if err != nil {
		return Policy{}, err
	}
	window, err := observationPeriod()
	if err != nil {
		return Policy{}, err
	}
	p := Policy{AutoCheck: interval > 0, CheckIntervalMinutes: interval.Minutes(), ObservationSeconds: window.Seconds()}
	if !p.AutoCheck {
		p.CheckIntervalMinutes = defaultCheckInterval.Minutes()
	}
	return p, nil
}

func (m *Manager) policyView(p Policy, custom bool) PolicySnapshot {
	view := PolicySnapshot{Policy: p, Customized: custom}
	// Bind writes to the values and their origin. Resetting to equal deployment values is still a change.
	b, _ := json.Marshal(struct {
		Policy
		Customized bool `json:"customized"`
	}{p, custom})
	view.Revision = digest(b)
	view.LauncherAppliesWindow = m.managed && os.Getenv("NEWPRESS_UPDATE_POLICY_PROTOCOL") == policyProtocol
	return view
}

func (m *Manager) policy() (PolicySnapshot, error) {
	var stored Policy
	err := readJSON(filepath.Join(m.root, "policy.json"), &stored)
	if errors.Is(err, os.ErrNotExist) {
		defaults, err := deploymentPolicy()
		if err != nil {
			return PolicySnapshot{}, err
		}
		return m.policyView(defaults, false), nil
	}
	if err != nil || stored.validate() != nil {
		return PolicySnapshot{}, errors.New("无法读取已保存的更新策略")
	}
	return m.policyView(stored, true), nil
}

// A nil next restores deployment defaults. The operation lock also covers discovery and cutover.
func (m *Manager) ConfigurePolicy(expected string, next *Policy) (PolicySnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return PolicySnapshot{}, errors.New("更新服务已停止")
	}
	lock, err := m.lock()
	if err != nil {
		return PolicySnapshot{}, err
	}
	defer lock.Close()
	current, err := m.policy()
	if err != nil {
		return PolicySnapshot{}, err
	}
	if expected != current.Revision {
		return PolicySnapshot{}, ErrPolicyConflict
	}
	var values Policy
	if next == nil {
		values, err = deploymentPolicy()
		if err != nil {
			return PolicySnapshot{}, err
		}
		err = removeRecord(m.root, "policy.json")
	} else {
		values = *next
		if err = values.validate(); err != nil {
			return PolicySnapshot{}, err
		}
		err = writeJSON(filepath.Join(m.root, "policy.json"), values)
	}
	if err != nil {
		return PolicySnapshot{}, errors.New("无法保存更新策略")
	}
	return m.policyView(values, next != nil), nil
}
