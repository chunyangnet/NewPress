// Package accessconfig keeps login and SMS credentials outside portable site settings.
package accessconfig

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"

	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/site"
)

var ErrConflict = errors.New("配置已被其他窗口修改，请重新加载后合并")

type Provider struct {
	ID          string `json:"id"`
	Enabled     bool   `json:"enabled"`
	ClientID    string `json:"client_id"`
	Secret      string `json:"secret,omitempty"`
	HasSecret   bool   `json:"has_secret,omitempty"`
	ClearSecret bool   `json:"clear_secret,omitempty"`
}

type Settings struct {
	Revision        string     `json:"revision"`
	CallbackBaseURL string     `json:"callback_base_url"`
	Providers       []Provider `json:"providers"`
	SMS             *SMS       `json:"sms"`
}

type Store struct {
	mu       sync.RWMutex
	path     string
	settings Settings
}

func revision() (string, error) {
	var b [24]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}

func NewStore(path string) (*Store, error) {
	r, err := revision()
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, settings: Settings{Revision: r, Providers: []Provider{}}}
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
	b, err := io.ReadAll(io.LimitReader(f, 131073))
	if err != nil || len(b) > 131072 {
		return nil, errors.New("登录与短信设置文件无法读取或过大")
	}
	var saved Settings
	if packagefs.DecodeJSONInto(bytes.NewReader(b), 131072, "login and SMS settings", &saved) != nil || saved.Revision == "" {
		return nil, errors.New("登录与短信设置文件格式错误")
	}
	next, err := normalize(saved, Settings{})
	if err != nil {
		return nil, err
	}
	s.settings = next
	return s, nil
}

func clone(s Settings, redact bool) Settings {
	s.Providers = append([]Provider{}, s.Providers...)
	if s.SMS != nil {
		p := *s.SMS
		s.SMS = &p
	}
	if redact {
		for i := range s.Providers {
			p := &s.Providers[i]
			p.HasSecret = p.Secret != ""
			p.Secret = ""
		}
		if s.SMS != nil {
			s.SMS.HasSecret = s.SMS.Secret != ""
			s.SMS.Secret = ""
		}
	}
	return s
}

func Redact(s Settings) Settings { return clone(s, true) }

// Runtime returns an independent copy of the configured credentials.
func (s *Store) Runtime() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.settings, false)
}

func ValidCallbackBase(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || len(raw) > 2048 || strings.ContainsAny(raw, "\\\r\n\t") {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http" && slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname())
}

func validCredential(raw string, max int) bool {
	return len(raw) <= max && strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func normalize(in, old Settings) (Settings, error) {
	in = clone(in, false)
	in.CallbackBaseURL = strings.TrimRight(strings.TrimSpace(in.CallbackBaseURL), "/")
	if in.CallbackBaseURL != "" && !ValidCallbackBase(in.CallbackBaseURL) {
		return Settings{}, errors.New("回调站点地址须为 HTTPS 域名，不含路径、查询或凭据；本地调试可用 HTTP localhost")
	}
	if len(in.Providers) > len(site.LoginProviderOrder) {
		return Settings{}, errors.New("第三方登录配置过多")
	}
	seen := map[string]bool{}
	for i := range in.Providers {
		p := &in.Providers[i]
		if !slices.Contains(site.LoginProviderOrder, p.ID) || seen[p.ID] {
			return Settings{}, errors.New("第三方登录服务未知或重复")
		}
		seen[p.ID] = true
		p.ClientID, p.Secret = strings.TrimSpace(p.ClientID), strings.TrimSpace(p.Secret)
		if p.ClearSecret && p.Secret != "" {
			return Settings{}, errors.New("清除密钥与填写新密钥不能同时选择")
		}
		for _, previous := range old.Providers {
			if p.ID == previous.ID && p.ClientID == previous.ClientID && p.Secret == "" && !p.ClearSecret {
				p.Secret = previous.Secret
			}
		}
		if !validCredential(p.ClientID, 200) || !validCredential(p.Secret, 4096) || p.Enabled && (p.ClientID == "" || p.Secret == "") {
			return Settings{}, errors.New(site.LoginProviderNames[p.ID] + "：启用前请填写应用 ID 和密钥；更换应用 ID 后须重新填写密钥")
		}
		p.HasSecret, p.ClearSecret = false, false
	}
	// Missing entries in a partial API write retain their current management state.
	for _, p := range old.Providers {
		if !seen[p.ID] {
			in.Providers = append(in.Providers, p)
		}
	}
	if in.SMS == nil && old.SMS != nil {
		sms := *old.SMS
		in.SMS = &sms
	}
	if in.SMS != nil {
		previous := SMS{}
		if old.SMS != nil {
			previous = *old.SMS
		}
		sms, err := normalizeSMS(*in.SMS, previous)
		if err != nil {
			return Settings{}, err
		}
		in.SMS = &sms
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
	next.Revision, err = revision()
	if err != nil {
		return Settings{}, err
	}
	if s.path != "" {
		b, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return Settings{}, err
		}
		if len(b) > 131072 {
			return Settings{}, errors.New("登录与短信配置不能超过 128 KiB")
		}
		if err = packagefs.MkdirAllNoSymlink(filepath.Dir(s.path), 0700); err != nil {
			return Settings{}, err
		}
		f, err := os.CreateTemp(filepath.Dir(s.path), ".access-settings-*.tmp")
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
