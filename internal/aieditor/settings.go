package aieditor

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

// AI credentials live in a private document, outside the exportable site settings.
// Only Snapshot may be returned to browsers; Resolve supplies secrets to the client.
type Provider struct {
	ID            string        `json:"id"`
	Protocol      string        `json:"protocol,omitempty"`
	APIVersion    string        `json:"api_version,omitempty"`
	Name          string        `json:"name"`
	BaseURL       string        `json:"base_url"`
	AllowInsecure bool          `json:"allow_insecure,omitempty"`
	APIKey        string        `json:"api_key,omitempty"`
	HasKey        bool          `json:"has_key,omitempty"`
	Models        []string      `json:"models"`
	DefaultModel  string        `json:"default_model"`
	Pricing       []ModelPrice  `json:"pricing,omitempty"`
	Balance       BalanceConfig `json:"balance"`
}

type Settings struct {
	Revision             string           `json:"revision"`
	DefaultProvider      string           `json:"default_provider"`
	DefaultImageProvider string           `json:"default_image_provider,omitempty"`
	Providers            []Provider       `json:"providers"`
	Telemetry            *TelemetryConfig `json:"telemetry,omitempty"`
}

type SettingsStore struct {
	mu       sync.RWMutex
	path     string
	settings Settings
}

var providerID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

const maxSettingsBytes = 512 << 10

func newRevision() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value[:])
}

func NewSettingsStore(path string) (*SettingsStore, error) {
	store := &SettingsStore{path: path, settings: Settings{Revision: newRevision(), Providers: []Provider{}}}
	if path == "" {
		return store, nil
	}
	file, _, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, errors.New("无法读取 AI 接口设置")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil || len(data) > maxSettingsBytes {
		return nil, errors.New("AI 接口设置文件过大或无法读取")
	}
	var saved Settings
	if json.Unmarshal(data, &saved) != nil || saved.Revision == "" {
		return nil, errors.New("AI 接口设置文件格式无效")
	}
	normalized, err := normalizeSettings(saved, Settings{})
	if err != nil {
		return nil, errors.New("AI 接口设置文件校验失败")
	}
	store.settings = normalized
	return store, nil
}

func cloneSettings(value Settings, redact bool) Settings {
	if value.Telemetry != nil {
		copy := *value.Telemetry
		value.Telemetry = &copy
	}
	value.Providers = append([]Provider{}, value.Providers...)
	for i := range value.Providers {
		p := &value.Providers[i]
		p.Models = append([]string{}, p.Models...)
		p.Pricing = clonePrices(p.Pricing)
		if redact {
			p.HasKey = p.APIKey != ""
			p.APIKey = ""
			p.Balance.HasToken = p.Balance.Token != ""
			p.Balance.Token = ""
		}
	}
	return value
}

func (s *SettingsStore) Snapshot() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSettings(s.settings, true)
}

func normalizeSettings(input, previous Settings) (Settings, error) {
	if len(input.Providers) > 20 {
		return Settings{}, failure("invalid_settings", "最多保存 20 个 AI 接口", http.StatusBadRequest)
	}
	input = cloneSettings(input, false)
	if input.Telemetry == nil {
		config := DefaultTelemetryConfig()
		if previous.Telemetry != nil {
			config = *previous.Telemetry
		}
		input.Telemetry = &config
	}
	if err := input.Telemetry.validate(); err != nil {
		return Settings{}, err
	}
	seen := map[string]bool{}
	for i := range input.Providers {
		p := &input.Providers[i]
		p.Name = strings.TrimSpace(p.Name)
		if !providerID.MatchString(p.ID) || seen[p.ID] || p.Name == "" || utf8.RuneCountInString(p.Name) > 80 {
			return Settings{}, failure("invalid_settings", "接口名称不能为空且最多 80 字，接口标识不可重复", http.StatusBadRequest)
		}
		seen[p.ID] = true
		if !validProtocol(p.Protocol) {
			return Settings{}, failure("invalid_protocol", "请选择受支持的接口协议", http.StatusBadRequest)
		}
		base, err := normalizeEndpointPolicy(p.BaseURL, p.Protocol, p.AllowInsecure)
		if err != nil {
			return Settings{}, err
		}
		p.BaseURL, p.APIKey, p.HasKey = base, strings.TrimSpace(p.APIKey), false
		p.APIVersion, err = normalizeAPIVersion(p.APIVersion, p.Protocol)
		if err != nil {
			return Settings{}, err
		}
		if p.APIKey == "" {
			for _, old := range previous.Providers {
				if old.ID == p.ID {
					if old.BaseURL != base || old.Protocol != p.Protocol {
						if old.APIKey != "" {
							return Settings{}, failure("key_required", "更换 API 地址或协议时请重新填写 Key；无鉴权服务请新增接口", http.StatusBadRequest)
						}
						continue
					}
					p.APIKey = old.APIKey
				}
			}
		}
		if !(p.APIKey == "" && p.AllowInsecure) && !validValue(p.APIKey, 4096) {
			return Settings{}, failure("invalid_key", "每个接口都需要有效的 API Key；已保存的接口留空可保留原 Key", http.StatusBadRequest)
		}
		if len(p.Models) == 0 || len(p.Models) > 100 {
			return Settings{}, failure("invalid_models", "每个接口请选择或填写 1–100 个模型", http.StatusBadRequest)
		}
		models := []string{}
		for _, model := range p.Models {
			model = strings.TrimSpace(model)
			if !validValue(model, 200) {
				return Settings{}, failure("invalid_model", "模型 ID 最多 200 字节，不能包含空格或换行", http.StatusBadRequest)
			}
			if err := validateModel(p.Protocol, model); err != nil {
				return Settings{}, err
			}
			if !slices.Contains(models, model) {
				models = append(models, model)
			}
		}
		p.Models = models
		p.DefaultModel = strings.TrimSpace(p.DefaultModel)
		if p.DefaultModel == "" {
			p.DefaultModel = models[0]
		}
		if !slices.Contains(models, p.DefaultModel) {
			return Settings{}, failure("invalid_model", "默认模型必须在该接口的模型列表中", http.StatusBadRequest)
		}
		if err := normalizeOperations(p, previous); err != nil {
			return Settings{}, err
		}
	}
	for _, image := range []bool{false, true} {
		selected := &input.DefaultProvider
		if image {
			selected = &input.DefaultImageProvider
		}
		available := []string{}
		for _, p := range input.Providers {
			if isImageProtocol(p.Protocol) == image {
				available = append(available, p.ID)
			}
		}
		if len(available) == 0 {
			*selected = ""
		} else if image && *selected == "" {
			*selected = available[0]
		} else if !slices.Contains(available, *selected) {
			return Settings{}, failure("invalid_provider", "请为写作和生图分别选择对应的默认接口", http.StatusBadRequest)
		}
	}
	return input, nil
}

func (s *SettingsStore) Save(input Settings) (Settings, error) {
	return s.SaveChecked(input, nil)
}

// SaveChecked refreshes external authority after waiting for the store lock and
// immediately before persistence. The callback must not call this store. A
// committed filesystem write is not rolled back by subsequent revocation.
func (s *SettingsStore) SaveChecked(input Settings, check func() error) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if check != nil {
		if err := check(); err != nil {
			return Settings{}, err
		}
	}
	if input.Revision == "" || input.Revision != s.settings.Revision {
		return Settings{}, failure("settings_conflict", "AI 设置已被其他页面更新，请重新加载后再保存", http.StatusConflict)
	}
	next, err := normalizeSettings(input, s.settings)
	if err != nil {
		return Settings{}, err
	}
	next.Revision = newRevision()
	if check != nil {
		if err := check(); err != nil {
			return Settings{}, err
		}
	}
	if s.path != "" {
		if err := writeSettings(s.path, next); err != nil {
			return Settings{}, failure("settings_write", "AI 设置未能保存到磁盘，请检查数据目录权限后重试", http.StatusInternalServerError)
		}
	}
	s.settings = next
	return cloneSettings(next, true), nil
}

func writeSettings(path string, settings Settings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxSettingsBytes {
		return errors.New("AI settings too large")
	}
	if err := packagefs.MkdirAllNoSymlink(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".ai-settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return packagefs.ReplaceRegularFileNoSymlink(file.Name(), path)
}

func (s *SettingsStore) Resolve(input Connection, requireModel bool) (Connection, error) {
	connection, err := s.resolve(input, requireModel)
	if err == nil && requireModel && !isTextProtocol(connection.Protocol) {
		return Connection{}, failure("wrong_protocol", "该接口用于生图，请选择写作接口", http.StatusBadRequest)
	}
	return connection, err
}

func (s *SettingsStore) ResolveImage(input Connection) (Connection, error) {
	connection, err := s.resolve(input, true)
	if err == nil && !isImageProtocol(connection.Protocol) {
		return Connection{}, failure("wrong_protocol", "请在 AI 设置中添加生图接口后选择", http.StatusBadRequest)
	}
	return connection, err
}

func (s *SettingsStore) resolve(input Connection, requireModel bool) (Connection, error) {
	if input.ProviderID == "" {
		return input.Validate(requireModel)
	}
	if input.APIKey != "" || input.BaseURL != "" || input.Protocol != "" || input.APIVersion != "" {
		return Connection{}, failure("ambiguous_connection", "已保存接口不能同时传入临时地址或 Key", http.StatusBadRequest)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.settings.Providers {
		if p.ID != input.ProviderID {
			continue
		}
		model := strings.TrimSpace(input.Model)
		if model == "" {
			model = p.DefaultModel
		}
		if requireModel && !slices.Contains(p.Models, model) {
			return Connection{}, failure("invalid_model", "该模型不在接口配置中，请重新选择或更新 AI 设置", http.StatusBadRequest)
		}
		connection := Connection{Protocol: p.Protocol, APIVersion: p.APIVersion, BaseURL: p.BaseURL, APIKey: p.APIKey, Model: model, StreamUsage: input.StreamUsage}
		if p.AllowInsecure {
			connection.approvedEndpoint = p.BaseURL
		}
		return connection.Validate(requireModel)
	}
	return Connection{}, failure("provider_not_found", "该接口已移除，请刷新接口列表后重新选择", http.StatusBadRequest)
}
