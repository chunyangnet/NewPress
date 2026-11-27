// Package verification owns human verification settings and short-lived proofs.
// Credentials are kept outside the exportable site settings document.
package verification

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"newpress.local/newpress/internal/packagefs"
)

var ErrConflict = errors.New("验证设置已被更新，请重新加载后再保存")

var Scenes = []string{"comment", "register", "recover", "login", "admin_login", "feedback"}

type ProviderInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Network     string `json:"network"`
	NetworkNote string `json:"network_note"`
	Description string `json:"description"`
	Docs        string `json:"docs"`
}

func Catalog() []ProviderInfo {
	return []ProviderInfo{
		{"local", "本地图片验证码", "local", "由本站生成，不依赖境外网络；适合基础防刷，强度低于专业风控服务。", "无需申请密钥，输入图片中的 5 位数字。", ""},
		{"geetest", "极验 4", "domestic", "国内服务，适合中国大陆访客；部署前仍需在目标运营商网络实测。", "滑块与智能验证，使用第四代 Captcha ID / Key。", "https://docs.geetest.com/gt4/start/"},
		{"tencent", "腾讯云验证码", "domestic", "国内服务，适合中国大陆访客；需开通验证码应用与云 API 权限。", "滑块与智能验证，服务端使用腾讯云 API 3.0 签名。", "https://cloud.tencent.com/document/product/1110/36926"},
		{"turnstile", "Cloudflare Turnstile", "test", "境外服务，国内连通性受地区、运营商影响；请实测，不保证稳定可用。", "托管式交互验证，通常无需识别图片。", "https://developers.cloudflare.com/turnstile/"},
		{"hcaptcha", "hCaptcha", "test", "境外服务，国内加载与图片挑战需实测，不建议作为国内站点唯一方案。", "图片挑战与风险识别，使用 Sitekey / Secret。", "https://docs.hcaptcha.com/"},
		{"recaptcha_v2", "Google reCAPTCHA v2", "restricted", "Google 域名在国内访问受限；可切换 recaptcha.net，但仍需实测全部挑战资源。", "复选框与图片挑战。", "https://developers.google.com/recaptcha/docs/display"},
		{"recaptcha_v3", "Google reCAPTCHA v3", "restricted", "国内不建议作为唯一方案；recaptcha.net 可改善部分网络访问，但不保证可用。", "无感评分验证，支持风险分数阈值。", "https://developers.google.com/recaptcha/docs/v3"},
	}
}

type Provider struct {
	ID             string  `json:"id"`
	SiteKey        string  `json:"site_key"`
	Secret         string  `json:"secret,omitempty"`
	HasSecret      bool    `json:"has_secret,omitempty"`
	SecretID       string  `json:"secret_id,omitempty"`
	APISecret      string  `json:"api_secret,omitempty"`
	HasAPISecret   bool    `json:"has_api_secret,omitempty"`
	RecaptchaNet   bool    `json:"recaptcha_net"`
	MinScore       float64 `json:"min_score"`
	ClearSecret    bool    `json:"clear_secret,omitempty"`
	ClearAPISecret bool    `json:"clear_api_secret,omitempty"`
}

type Rule struct {
	Enabled     bool   `json:"enabled"`
	Provider    string `json:"provider"`
	SkipMembers bool   `json:"skip_members"`
}

type Settings struct {
	Revision        string          `json:"revision"`
	Enabled         bool            `json:"enabled"`
	DefaultProvider string          `json:"default_provider"`
	Theme           string          `json:"theme"`
	Language        string          `json:"language"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
	Scenes          map[string]Rule `json:"scenes"`
	Providers       []Provider      `json:"providers"`
}

type Store struct {
	mu       sync.RWMutex
	path     string
	settings Settings
}

func randomID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, settings: Settings{Revision: randomID(), DefaultProvider: "local", Theme: "auto", Language: "zh-CN", TimeoutSeconds: 8, Scenes: map[string]Rule{}, Providers: []Provider{}}}
	for _, scene := range Scenes {
		s.settings.Scenes[scene] = Rule{Enabled: scene == "comment" || scene == "register" || scene == "recover"}
	}
	if path == "" {
		return s, nil
	}
	f, _, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 {
		return nil, errors.New("验证设置文件无法读取或过大")
	}
	var saved Settings
	if json.Unmarshal(b, &saved) != nil || saved.Revision == "" {
		return nil, errors.New("验证设置文件格式错误")
	}
	next, err := normalize(saved, Settings{})
	if err != nil {
		return nil, err
	}
	s.settings = next
	return s, nil
}

func clone(s Settings, redact bool) Settings {
	s.Scenes = maps.Clone(s.Scenes)
	s.Providers = append([]Provider{}, s.Providers...)
	if redact {
		for i := range s.Providers {
			p := &s.Providers[i]
			p.HasSecret = p.Secret != ""
			p.HasAPISecret = p.APISecret != ""
			p.Secret = ""
			p.APISecret = ""
		}
	}
	return s
}
func (s *Store) Snapshot() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.settings, true)
}
func (s *Store) runtime() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.settings, false)
}
func (s *Store) Enabled() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.settings.Enabled }

func validProvider(id string) bool {
	for _, p := range Catalog() {
		if p.ID == id {
			return true
		}
	}
	return false
}
func ValidScene(scene string) bool { return slices.Contains(Scenes, scene) }
func providerFor(s Settings, id string) Provider {
	for _, p := range s.Providers {
		if p.ID == id {
			return p
		}
	}
	return Provider{ID: id}
}
func configured(p Provider) bool {
	return p.ID == "local" || (p.SiteKey != "" && p.Secret != "" && (p.ID != "tencent" || (p.SecretID != "" && p.APISecret != "")))
}

func normalize(in, old Settings) (Settings, error) {
	in = clone(in, false)
	if !validProvider(in.DefaultProvider) || !slices.Contains([]string{"auto", "light", "dark"}, in.Theme) || !slices.Contains([]string{"auto", "zh-CN", "en"}, in.Language) || in.TimeoutSeconds < 3 || in.TimeoutSeconds > 15 {
		return Settings{}, errors.New("请选择有效的默认服务、语言、外观与 3–15 秒超时时间")
	}
	if len(in.Providers) > 6 || len(in.Scenes) != len(Scenes) {
		return Settings{}, errors.New("验证服务或场景配置不完整")
	}
	seen := map[string]bool{}
	for i := range in.Providers {
		p := &in.Providers[i]
		if !validProvider(p.ID) || p.ID == "local" || seen[p.ID] {
			return Settings{}, errors.New("验证服务未知或重复")
		}
		seen[p.ID] = true
		previous := providerFor(old, p.ID)
		p.SiteKey = strings.TrimSpace(p.SiteKey)
		p.Secret = strings.TrimSpace(p.Secret)
		p.APISecret = strings.TrimSpace(p.APISecret)
		p.SecretID = strings.TrimSpace(p.SecretID)
		if p.Secret == "" && !p.ClearSecret && p.SiteKey == previous.SiteKey {
			p.Secret = previous.Secret
		}
		if p.APISecret == "" && !p.ClearAPISecret && p.SecretID == previous.SecretID {
			p.APISecret = previous.APISecret
		}
		for _, value := range []string{p.SiteKey, p.Secret, p.SecretID, p.APISecret} {
			if len(value) > 2048 || strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
				return Settings{}, errors.New("密钥或标识不能包含空白，且最长为 2048 字节")
			}
		}
		if p.ID == "tencent" && p.SiteKey != "" {
			id, err := strconv.ParseInt(p.SiteKey, 10, 64)
			if err != nil || id <= 0 {
				return Settings{}, errors.New("腾讯云 CaptchaAppId 必须是正整数")
			}
		}
		if p.MinScore == 0 {
			p.MinScore = 0.5
		}
		if p.MinScore < 0.1 || p.MinScore > 0.9 {
			return Settings{}, errors.New("reCAPTCHA 风险阈值必须在 0.1–0.9 之间")
		}
		p.ClearSecret = false
		p.ClearAPISecret = false
		p.HasSecret = false
		p.HasAPISecret = false
	}
	for scene, rule := range in.Scenes {
		if !ValidScene(scene) || (rule.Provider != "" && !validProvider(rule.Provider)) || (rule.SkipMembers && scene != "comment" && scene != "feedback") {
			return Settings{}, errors.New("场景配置不正确")
		}
		id := rule.Provider
		if id == "" {
			id = in.DefaultProvider
		}
		if in.Enabled && rule.Enabled && !configured(providerFor(in, id)) {
			return Settings{}, errors.New("启用的场景缺少验证服务密钥，请先完成配置")
		}
	}
	return in, nil
}

func (s *Store) Save(in Settings) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Revision == "" || in.Revision != s.settings.Revision {
		return Settings{}, ErrConflict
	}
	next, err := normalize(in, s.settings)
	if err != nil {
		return Settings{}, err
	}
	next.Revision = randomID()
	if s.path != "" {
		b, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return Settings{}, err
		}
		if err = packagefs.MkdirAllNoSymlink(filepath.Dir(s.path), 0700); err != nil {
			return Settings{}, err
		}
		f, err := os.CreateTemp(filepath.Dir(s.path), ".verification-*.tmp")
		if err != nil {
			return Settings{}, err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(b)
		}
		if err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = packagefs.ReplaceRegularFileNoSymlink(f.Name(), s.path)
		}
		if err != nil {
			return Settings{}, err
		}
	}
	s.settings = next
	return clone(next, true), nil
}
