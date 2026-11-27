// Package update manages signed public/private releases and a portable supervisor.
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"runtime"
	"strings"
	"time"

	"newpress.local/newpress/internal/version"
)

const MaxPackageBytes int64 = 96 << 20 // Gitee raw API accepts files below 100 MB.
const Protocol = 1

var ErrBusy = errors.New("已有更新任务正在运行")
var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var devicePattern = regexp.MustCompile(`^(COM|LPT)[1-9]$`)

type Source struct {
	Official bool   `json:"official,omitempty"`
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	Ref      string `json:"ref"`
	Manifest string `json:"manifest"`
	Token    string `json:"token,omitempty"`
}

func (s Source) validate() error {
	if s.Official {
		s.Token = "" // Environment-only private credentials never accompany public requests.
		if s != officialSource() {
			return errors.New("官方更新源使用内置仓库和测试版通道，无需访问令牌")
		}
		return nil
	}
	if !segmentPattern.MatchString(s.Owner) || !segmentPattern.MatchString(s.Repo) {
		return errors.New("请填写有效的 Gitee 空间和仓库名称")
	}
	if len(s.Ref) == 0 || len(s.Ref) > 128 || strings.ContainsAny(s.Ref, "\r\n\x00") {
		return errors.New("分支或标签格式不正确")
	}
	if !safePath(s.Manifest) || !strings.HasSuffix(s.Manifest, ".json") {
		return errors.New("更新清单必须是仓库内的 JSON 文件路径")
	}
	if len(s.Token) == 0 || len(s.Token) > 512 || strings.ContainsAny(s.Token, "\r\n\x00") {
		return errors.New("请配置 Gitee 私有仓库访问令牌")
	}
	return nil
}

type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Product  string    `json:"product"`
	Sequence int64     `json:"sequence"`
	Expires  time.Time `json:"expires"`
	Schema   int       `json:"schema"`
	Version  string    `json:"version"`
	Notes    string    `json:"notes"`
	// Online updates must preserve the previous executable's data contract.
	DataCompatible bool       `json:"data_compatible"`
	Artifacts      []Artifact `json:"artifacts"`
}

// ValidateContents checks publication contents independently of the host platform.
// Envelope identity, expiry, signature and sequence are validated by callers.
func (m Manifest) ValidateContents() error {
	if m.Schema != manifestSchema || !ValidVersion(m.Version) || len(m.Notes) > 16000 {
		return errors.New("更新清单格式或版本不受支持")
	}
	if (m.DataCompatible && len(m.Artifacts) == 0) || len(m.Artifacts) > 16 {
		return errors.New("更新清单缺少有效的平台文件列表")
	}
	seen := map[string]bool{}
	for _, a := range m.Artifacts {
		key := a.OS + "/" + a.Arch
		if !segmentPattern.MatchString(a.OS) || !segmentPattern.MatchString(a.Arch) || seen[key] || !safePath(a.Path) || !strings.HasSuffix(a.Path, ".zip") || a.Size <= 0 || a.Size > MaxPackageBytes || !digestPattern.MatchString(a.SHA256) {
			return errors.New("更新包平台、路径、大小或摘要不正确")
		}
		seen[key] = true
	}
	return nil
}

func (m Manifest) artifact() (Artifact, error) {
	if err := m.ValidateContents(); err != nil {
		return Artifact{}, err
	}
	if !m.DataCompatible {
		if len(m.Artifacts) == 0 {
			return Artifact{}, errors.New("发布者已撤回此版本，已阻止在线安装")
		}
		return Artifact{}, errors.New("此版本需要离线数据迁移，不能在线安装")
	}
	for _, a := range m.Artifacts {
		if a.OS == runtime.GOOS && a.Arch == runtime.GOARCH {
			return a, nil
		}
	}
	return Artifact{}, fmt.Errorf("此版本没有 %s/%s 更新包", runtime.GOOS, runtime.GOARCH)
}

func safePath(s string) bool {
	if s == "" || len(s) > 240 || path.Clean(s) != s || strings.HasPrefix(s, "/") || s == "." {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		reserved := stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || devicePattern.MatchString(stem)
		if part == ".." || part == "." || strings.HasSuffix(part, ".") || reserved || !segmentPattern.MatchString(part) {
			return false
		}
	}
	return true
}

// ValidVersion reports whether a core release uses the supported SemVer form
// of at most 64 characters. Legacy v prefixes remain valid; build metadata is
// excluded from the core release format.
func ValidVersion(value string) bool {
	parsed, err := version.Parse(value)
	return err == nil && parsed.Build == ""
}

func newer(candidate, current string) bool {
	if !ValidVersion(candidate) || !ValidVersion(current) {
		return false
	}
	comparison, err := version.Compare(candidate, current)
	return err == nil && comparison > 0
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

type Log struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}
type State struct {
	ID             string    `json:"id"`
	Phase          string    `json:"phase"`
	Percent        int       `json:"percent"`
	Downloaded     int64     `json:"downloaded"`
	Total          int64     `json:"total"`
	Target         string    `json:"target"`
	Previous       string    `json:"previous,omitempty"`
	ProtectedUntil time.Time `json:"protected_until,omitzero"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Logs           []Log     `json:"logs"`
}

func (s State) Busy() bool {
	switch s.Phase {
	case "checking", "downloading", "verifying", "staging", "queued", "stopping", "backup", "starting", "observing", "rollback":
		return true
	}
	return false
}

// Keep extension snapshots and the recovery receipt intact until a late crash can no longer undo them.
func (s State) BlocksChanges() bool {
	return s.Busy() || (s.Phase == "completed" && time.Now().Before(s.ProtectedUntil))
}

func (s *State) step(phase string, percent int, message string) {
	s.Phase, s.Percent, s.UpdatedAt = phase, percent, time.Now().UTC()
	if message != "" {
		level := "info"
		if phase == "failed" {
			level = "error"
		}
		if phase == "rollback" || phase == "rolled_back" {
			level = "warning"
		}
		s.Logs = append(s.Logs, Log{s.UpdatedAt, level, message})
		if len(s.Logs) > 200 {
			s.Logs = s.Logs[len(s.Logs)-200:]
		}
	}
}

type Release struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	BinaryHash string `json:"binary_hash"`
}
type Transaction struct {
	Generation string    `json:"generation,omitempty"`
	Expires    time.Time `json:"expires,omitempty"`
	Candidate  Release   `json:"candidate"`
	Previous   Release   `json:"previous"`
	State      State     `json:"state"`
}
type Available struct {
	Mirrors     []publicRevision `json:"mirrors,omitempty"`
	KeyID       string           `json:"key_id"`
	PayloadHash string           `json:"payload_hash"`
	Manifest    Manifest         `json:"manifest"`
	Commit      string           `json:"commit"`
	Fingerprint string           `json:"fingerprint"`
}
type Snapshot struct {
	Policy           PolicySnapshot `json:"policy"`
	TrustedPublisher bool           `json:"trusted_publisher"`
	Revision         string         `json:"revision,omitempty"`
	Discovery        Discovery      `json:"discovery"`
	Current          string         `json:"current"`
	Platform         string         `json:"platform"`
	Environment      string         `json:"environment"`
	Managed          bool           `json:"managed"`
	Configured       bool           `json:"configured"`
	TokenSet         bool           `json:"token_set"`
	EnvironmentToken bool           `json:"environment_token"`
	Source           Source         `json:"source"`
	Available        *Manifest      `json:"available,omitempty"`
	State            State          `json:"state"`
}
