package aieditor

import (
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

type TelemetryConfig struct {
	Enabled       bool `json:"enabled"`
	Details       bool `json:"details"`
	Costs         bool `json:"costs"`
	Balances      bool `json:"balances"`
	Charts        bool `json:"charts"`
	RetentionDays int  `json:"retention_days"`
	MaxRecords    int  `json:"max_records"`
}

func DefaultTelemetryConfig() TelemetryConfig {
	return TelemetryConfig{Enabled: true, Details: true, Costs: true, Charts: true, RetentionDays: 30, MaxRecords: 5000}
}
func (c *TelemetryConfig) validate() error {
	if c.RetentionDays == 0 {
		c.RetentionDays = 30
	}
	if c.MaxRecords == 0 {
		c.MaxRecords = 5000
	}
	if !slices.Contains([]int{7, 30, 90}, c.RetentionDays) || c.MaxRecords < 100 || c.MaxRecords > 10000 {
		return failure("invalid_telemetry", "统计保留期请选择 7、30 或 90 天，明细上限为 100–10000 条", http.StatusBadRequest)
	}
	return nil
}
func (s *SettingsStore) Operations() TelemetryConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings.Telemetry == nil {
		return DefaultTelemetryConfig()
	}
	return *s.settings.Telemetry
}

// Prices are explicitly entered by the administrator. A missing price is not
// zero. Token prices are per million; image and request modes are flat rates.
type ModelPrice struct {
	Model      string   `json:"model"`
	Enabled    bool     `json:"enabled"`
	Currency   string   `json:"currency"`
	Mode       string   `json:"mode"`
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
	Flat       *float64 `json:"flat,omitempty"`
	Multiplier float64  `json:"multiplier"`
}

func clonePrices(prices []ModelPrice) []ModelPrice {
	out := append([]ModelPrice(nil), prices...)
	for i := range out {
		for _, ptr := range []**float64{&out[i].Input, &out[i].Output, &out[i].CacheRead, &out[i].CacheWrite, &out[i].Flat} {
			if *ptr != nil {
				v := **ptr
				*ptr = &v
			}
		}
	}
	return out
}

type BalanceConfig struct {
	Enabled       bool    `json:"enabled"`
	Format        string  `json:"format"`
	Path          string  `json:"path,omitempty"`
	Auth          string  `json:"auth,omitempty"`
	Token         string  `json:"token,omitempty"`
	HasToken      bool    `json:"has_token,omitempty"`
	ClearToken    bool    `json:"clear_token,omitempty"`
	UserID        string  `json:"user_id,omitempty"`
	RemainingPath string  `json:"remaining_path,omitempty"`
	UsedPath      string  `json:"used_path,omitempty"`
	TotalPath     string  `json:"total_path,omitempty"`
	Currency      string  `json:"currency,omitempty"`
	Divisor       float64 `json:"divisor,omitempty"`
	CacheSeconds  int     `json:"cache_seconds"`
}

var jsonFieldPath = regexp.MustCompile(`^[a-zA-Z0-9_-]+(\.[a-zA-Z0-9_-]+){0,15}$`)
var balanceUserID = regexp.MustCompile(`^[0-9]{1,20}$`)
var currencies = []string{"USD", "CNY", "EUR", "GBP", "JPY"}

func normalizeOperations(p *Provider, previous Settings) error {
	invalid := func(message string) error { return failure("invalid_operations", message, http.StatusBadRequest) }
	if len(p.Pricing) > 100 {
		return invalid("每个接口最多配置 100 个模型价格")
	}
	seen := map[string]bool{}
	for i := range p.Pricing {
		price := &p.Pricing[i]
		if !slices.Contains(p.Models, price.Model) || seen[price.Model] {
			return invalid("价格配置的模型必须在常用模型中且不可重复")
		}
		seen[price.Model] = true
		if price.Currency == "" {
			price.Currency = "USD"
		}
		if price.Mode == "" {
			price.Mode = "tokens"
		}
		if price.Multiplier == 0 {
			price.Multiplier = 1
		}
		if !slices.Contains(currencies, price.Currency) || !slices.Contains([]string{"tokens", "image", "request"}, price.Mode) || math.IsNaN(price.Multiplier) || math.IsInf(price.Multiplier, 0) || price.Multiplier < 0.001 || price.Multiplier > 1000 {
			return invalid("请检查计价币种、方式和倍率（0.001–1000）")
		}
		for _, value := range []*float64{price.Input, price.Output, price.CacheRead, price.CacheWrite, price.Flat} {
			if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 1000000) {
				return invalid("价格须在 0–1000000 之间；留空表示未配置")
			}
		}
	}
	b := &p.Balance
	b.Token = strings.TrimSpace(b.Token)
	if b.ClearToken {
		b.Token = ""
	} else if b.Token == "" {
		for _, old := range previous.Providers {
			if old.ID == p.ID && old.Balance.Token != "" {
				if old.BaseURL != p.BaseURL {
					return invalid("更换地址后请重新填写余额查询令牌，或勾选清除令牌")
				}
				b.Token = old.Balance.Token
			}
		}
	}
	b.ClearToken, b.HasToken = false, false
	if b.Token != "" && !validValue(b.Token, 4096) {
		return invalid("余额查询令牌不能包含空白字符，最多 4096 字节")
	}
	if b.Format == "" {
		b.Format = "custom"
	}
	if b.Auth == "" {
		b.Auth = "bearer"
	}
	if b.Currency == "" {
		b.Currency = "USD"
	}
	if b.CacheSeconds == 0 {
		b.CacheSeconds = 300
	}
	if b.Divisor == 0 {
		b.Divisor = 1
		if b.Format == "newapi" {
			b.Divisor = 500000
		}
	}
	if !slices.Contains([]string{"custom", "deepseek", "siliconflow", "openrouter", "sub2api", "newapi"}, b.Format) || !slices.Contains([]string{"bearer", "x-api-key", "api-key", "x-goog-api-key", "none"}, b.Auth) || !slices.Contains(currencies, b.Currency) || b.CacheSeconds < 60 || b.CacheSeconds > 86400 || math.IsNaN(b.Divisor) || math.IsInf(b.Divisor, 0) || b.Divisor < 0.000001 || b.Divisor > 1e12 {
		return invalid("请检查余额格式、鉴权、币种、换算除数与缓存时间（60–86400 秒）")
	}
	if b.UserID != "" && !balanceUserID.MatchString(b.UserID) {
		return invalid("New API 用户 ID 须为数字")
	}
	if b.Path != "" {
		u, err := url.Parse(b.Path)
		if err != nil || len(b.Path) > 512 || !strings.HasPrefix(b.Path, "/") || strings.Contains(b.Path, "//") || strings.ContainsAny(b.Path, "\\%\r\n\t") || u.Host != "" || u.Scheme != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || strings.Contains(b.Path, "..") {
			return invalid("余额查询路径须为本站点内的绝对路径，不含域名、查询参数或跳转")
		}
	}
	for _, path := range []string{b.RemainingPath, b.UsedPath, b.TotalPath} {
		if path != "" && (len(path) > 200 || !jsonFieldPath.MatchString(path)) {
			return invalid("JSON 字段路径格式为 data.balance，数组可写为 data.0.balance")
		}
	}
	if b.Enabled && b.Format == "custom" && (b.Path == "" || b.RemainingPath == "" && (b.UsedPath == "" || b.TotalPath == "")) {
		return invalid("自定义余额查询需要路径和剩余额度字段，或总额与已用字段")
	}
	if b.Enabled && b.Format == "newapi" && (b.Token == "" || b.UserID == "") {
		return invalid("New API 余额查询需要独立访问令牌及用户 ID")
	}
	return nil
}

// Captured at request start so later edits cannot alter historical estimates.
func (s *SettingsStore) Accounting(providerID, model string) (string, *ModelPrice) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.settings.Providers {
		if p.ID == providerID {
			for _, price := range clonePrices(p.Pricing) {
				if price.Model == model && price.Enabled {
					return p.Name, &price
				}
			}
			return p.Name, nil
		}
	}
	return "临时接口", nil
}

type CostEstimate struct {
	Amount   *float64    `json:"amount,omitempty"`
	Currency string      `json:"currency,omitempty"`
	Complete bool        `json:"complete"`
	Pricing  *ModelPrice `json:"pricing,omitempty"`
}

func EstimateCost(price *ModelPrice, usage Usage, kind, status string, images int) CostEstimate {
	result := CostEstimate{}
	if price == nil || !price.Enabled {
		return result
	}
	result.Currency, result.Pricing = price.Currency, price
	sum, known, complete := 0.0, false, true
	add := func(count *int, rate *float64) {
		if count == nil || rate == nil {
			complete = false
			return
		}
		if *count < 0 {
			complete = false
			return
		}
		sum += float64(*count) * *rate / 1e6
		known = true
	}
	switch price.Mode {
	case "image", "request":
		count := 1
		if price.Mode == "image" {
			count = images
			if kind != "image" || images == 0 {
				return result
			}
		} else if status != "success" {
			return result
		}
		if price.Flat != nil {
			sum = float64(count) * *price.Flat
			known = true
		} else {
			complete = false
		}
	default:
		if usage.PromptTokens != nil {
			normal := *usage.PromptTokens
			for _, part := range []struct {
				count *int
				rate  *float64
			}{{usage.CachedTokens, price.CacheRead}, {usage.CacheCreationTokens, price.CacheWrite}} {
				if part.count != nil && *part.count > 0 {
					if *part.count > normal {
						complete = false
						normal = -1
						break
					}
					normal -= *part.count
					rate := part.rate
					if rate == nil {
						rate = price.Input
					}
					add(part.count, rate)
				}
			}
			if normal >= 0 {
				add(&normal, price.Input)
			} else {
				return result
			}
		} else {
			complete = false
		}
		add(usage.CompletionTokens, price.Output)
		if usage.ToolPromptTokens != nil && *usage.ToolPromptTokens > 0 {
			add(usage.ToolPromptTokens, price.Input)
		}
	}
	if known {
		amount := sum * price.Multiplier
		if !math.IsNaN(amount) && !math.IsInf(amount, 0) {
			result.Amount = &amount
		}
	}
	result.Complete = known && complete && status == "success"
	return result
}
