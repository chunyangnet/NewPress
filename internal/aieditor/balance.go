package aieditor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/safehttp"
)

type BalanceResult struct {
	Remaining *float64  `json:"remaining,omitempty"`
	Used      *float64  `json:"used,omitempty"`
	Total     *float64  `json:"total,omitempty"`
	Currency  string    `json:"currency"`
	CheckedAt time.Time `json:"checked_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Cached    bool      `json:"cached"`
}
type balanceEntry struct {
	fingerprint [32]byte
	done        chan struct{}
	result      BalanceResult
	err         error
	expires     time.Time
}
type BalanceService struct {
	mu      sync.Mutex
	entries map[string]*balanceEntry
	slots   chan struct{}
	HTTP    *http.Client
}

func NewBalanceService() *BalanceService {
	return &BalanceService{entries: map[string]*balanceEntry{}, slots: make(chan struct{}, 2), HTTP: safehttp.Client(10 * time.Second)}
}
func (s *SettingsStore) balanceProvider(id string) (Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.settings.Telemetry == nil || !s.settings.Telemetry.Balances {
		return Provider{}, failure("balance_disabled", "请先在 AI 设置中开启余额探测", 400)
	}
	for _, p := range s.settings.Providers {
		if p.ID == id {
			if !p.Balance.Enabled {
				return Provider{}, failure("balance_disabled", "此接口未启用余额探测", 400)
			}
			return p, nil
		}
	}
	return Provider{}, failure("provider_not_found", "接口已移除，请刷新页面", 404)
}
func (s *BalanceService) Query(ctx context.Context, settings *SettingsStore, id string) (BalanceResult, error) {
	p, err := settings.balanceProvider(id)
	if err != nil {
		return BalanceResult{}, err
	}
	encoded, _ := json.Marshal(struct {
		Base, Key string
		Approved  bool
		Config    BalanceConfig
	}{p.BaseURL, p.APIKey, p.AllowInsecure, p.Balance})
	fingerprint := sha256.Sum256(encoded)
	s.mu.Lock()
	if e := s.entries[id]; e != nil && e.fingerprint == fingerprint {
		if e.done != nil {
			done := e.done
			s.mu.Unlock()
			select {
			case <-done:
				return s.Query(ctx, settings, id)
			case <-ctx.Done():
				return BalanceResult{}, ctx.Err()
			}
		}
		if time.Now().Before(e.expires) {
			result := e.result
			result.Cached = true
			s.mu.Unlock()
			return result, e.err
		}
	}
	if len(s.entries) >= 20 {
		for key, e := range s.entries {
			if e.done == nil {
				delete(s.entries, key)
			}
		}
	}
	e := &balanceEntry{fingerprint: fingerprint, done: make(chan struct{})}
	s.entries[id] = e
	s.mu.Unlock()
	// This query is shared by concurrent callers. One browser disconnect must
	// not cancel a second browser's request; the worker has its own hard timeout.
	queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		e.result, e.err = s.fetch(queryCtx, p)
		<-s.slots
	case <-queryCtx.Done():
		e.err = failure("balance_busy", "余额查询繁忙，请稍后再试", 429)
	}
	ttl := time.Duration(p.Balance.CacheSeconds) * time.Second
	if e.err != nil {
		ttl = time.Minute
	}
	s.mu.Lock()
	e.expires = time.Now().Add(ttl)
	e.result.ExpiresAt = e.expires
	close(e.done)
	e.done = nil
	s.mu.Unlock()
	return e.result, e.err
}
func balancePath(p Provider) string {
	if p.Balance.Path != "" {
		return p.Balance.Path
	}
	switch p.Balance.Format {
	case "deepseek":
		return "/user/balance"
	case "siliconflow":
		return "/v1/user/info"
	case "openrouter":
		return "/api/v1/credits"
	case "sub2api":
		return "/v1/usage"
	case "newapi":
		return "/api/user/self"
	}
	return ""
}
func (s *BalanceService) fetch(ctx context.Context, p Provider) (BalanceResult, error) {
	invalid := func() (BalanceResult, error) {
		return BalanceResult{}, failure("balance_format", "未找到有效余额，请检查预设、JSON 字段路径与币种", 502)
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return invalid()
	}
	u.Path = balancePath(p)
	u.RawPath = ""
	u.RawQuery = ""
	client := s.HTTP
	if p.AllowInsecure {
		// The administrator authorizes this explicit path on the same origin by
		// enabling the balance config. The generation endpoint remains confined.
		client, err = safehttp.EndpointClient(u.String(), 10*time.Second, 10*time.Second)
		if err != nil {
			return invalid()
		}
		defer client.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return invalid()
	}
	token := p.Balance.Token
	if token == "" {
		token = p.APIKey
	}
	auth := p.Balance.Auth
	if p.Balance.Format != "custom" {
		auth = "bearer"
	}
	if token != "" {
		switch auth {
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+token)
		case "none":
		default:
			req.Header.Set(auth, token)
		}
	}
	if p.Balance.Format == "newapi" {
		req.Header.Set("New-Api-User", p.Balance.UserID)
	}
	req.Header.Set("Accept", "application/json")
	// Always disable redirects, including custom injected test transports.
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := copy.Do(req)
	if err != nil {
		return BalanceResult{}, failure("balance_connection", "余额查询连接失败或超时，请检查接口配置", 502)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return BalanceResult{}, failure("balance_rejected", "余额接口拒绝请求，请核对令牌权限、用户 ID 和查询路径", 502)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 256<<10+1))
	if err != nil || len(data) > 256<<10 {
		return invalid()
	}
	result, err := parseBalance(data, p.Balance)
	if err != nil {
		return invalid()
	}
	result.CheckedAt = time.Now()
	return result, nil
}
func valueAt(root any, path string) any {
	if path == "" {
		return nil
	}
	current := root
	for _, part := range strings.Split(path, ".") {
		switch v := current.(type) {
		case map[string]any:
			current = v[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(v) {
				return nil
			}
			current = v[i]
		default:
			return nil
		}
	}
	return current
}
func balanceNumber(value any) *float64 {
	var n float64
	var err error
	switch v := value.(type) {
	case json.Number:
		n, err = v.Float64()
	case string:
		n, err = strconv.ParseFloat(v, 64)
	default:
		return nil
	}
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 1e12 {
		return nil
	}
	return &n
}
func parseBalance(data []byte, b BalanceConfig) (BalanceResult, error) {
	var root any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		return BalanceResult{}, errors.New("invalid JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return BalanceResult{}, errors.New("trailing JSON")
	}
	for _, path := range []string{"success", "isValid"} {
		if valueAt(root, path) == false {
			return BalanceResult{}, errors.New("invalid account")
		}
	}
	result := BalanceResult{Currency: b.Currency}
	divisor := b.Divisor
	if divisor == 0 {
		divisor = 1
	}
	remaining, used, total := b.RemainingPath, b.UsedPath, b.TotalPath
	switch b.Format {
	case "deepseek":
		remaining, used, total, divisor = "", "", "", 1
		list, _ := valueAt(root, "balance_infos").([]any)
		for _, item := range list {
			if valueAt(item, "currency") == b.Currency {
				result.Remaining = balanceNumber(valueAt(item, "total_balance"))
				break
			}
		}
	case "siliconflow":
		remaining = "data.totalBalance"
		result.Currency = "CNY"
		divisor = 1
	case "openrouter":
		total, used = "data.total_credits", "data.total_usage"
		result.Currency = "USD"
		divisor = 1
	case "sub2api":
		remaining, used, total = "remaining", "quota.used", "quota.limit"
		divisor = 1
		unit, _ := valueAt(root, "unit").(string)
		if unit == "" {
			unit, _ = valueAt(root, "quota.unit").(string)
		}
		if unit != "" {
			if !containsCurrency(unit) {
				return result, errors.New("unknown currency")
			}
			result.Currency = unit
		}
	case "newapi":
		remaining, used = "data.quota", "data.used_quota"
		if b.Divisor == 0 {
			divisor = 500000
		}
	}
	if remaining != "" {
		result.Remaining = balanceNumber(valueAt(root, remaining))
	}
	result.Used = balanceNumber(valueAt(root, used))
	result.Total = balanceNumber(valueAt(root, total))
	for _, v := range []*float64{result.Remaining, result.Used, result.Total} {
		if v != nil {
			*v /= divisor
		}
	}
	if result.Used != nil && *result.Used < 0 || result.Total != nil && *result.Total < 0 {
		return result, errors.New("invalid amount")
	}
	if b.Format == "sub2api" && result.Total != nil && *result.Total == 0 {
		result.Total = nil
		result.Used = nil
	}
	if result.Remaining == nil && result.Total != nil && result.Used != nil {
		n := *result.Total - *result.Used
		result.Remaining = &n
	}
	if b.Format == "newapi" && result.Remaining != nil && result.Used != nil {
		n := *result.Remaining + *result.Used
		result.Total = &n
	}
	if result.Remaining == nil {
		return result, errors.New("missing balance")
	}
	return result, nil
}
func containsCurrency(s string) bool {
	for _, c := range currencies {
		if c == s {
			return true
		}
	}
	return false
}
