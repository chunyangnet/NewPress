package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/virtualcurrency"
)

func (s *Server) registerCurrencyRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/currency/{$}", s.adminCurrencyPage)
	mux.HandleFunc("POST /admin/currency/{$}", s.adminCurrencyForm)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		mux.HandleFunc(method+" /admin/currency/{path...}", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	}
}

type currencyContextKey struct{}
type currencyContext struct {
	key   string
	store virtualcurrency.Store
}

// Resolve only after authentication. An invalid or missing explicit key must
// never become a transaction against the main ledger.
func (s *Server) currencyUnavailable(w http.ResponseWriter, r *http.Request) bool {
	key := r.PathValue("currency")
	if key == "" && r.URL.Path == "/admin/currency/" {
		key = r.URL.Query().Get("currency")
	}
	if key == "" {
		key = virtualcurrency.DefaultCurrencyKey
	}
	var store virtualcurrency.Store
	var err error
	if s.currencyCatalog != nil {
		store, err = s.currencyCatalog.Store(r.Context(), key)
	} else if key == virtualcurrency.DefaultCurrencyKey && s.currency != nil {
		store = s.currency
	} else {
		err = virtualcurrency.ErrStoreUnavailable
	}
	if err != nil {
		s.currencyError(w, r, err)
		return true
	}
	*r = *r.WithContext(context.WithValue(r.Context(), currencyContextKey{}, currencyContext{key: key, store: store}))
	return false
}
func currencyKey(r *http.Request) string {
	if selected, ok := r.Context().Value(currencyContextKey{}).(currencyContext); ok {
		return selected.key
	}
	return virtualcurrency.DefaultCurrencyKey
}
func (s *Server) currencyStore(r *http.Request) virtualcurrency.Store {
	if selected, ok := r.Context().Value(currencyContextKey{}).(currencyContext); ok {
		return selected.store
	}
	return s.currency
}
func (s *Server) currencyRewardService(r *http.Request) *virtualcurrency.Service {
	return &virtualcurrency.Service{Store: s.currencyStore(r), Users: s.auth, Content: s.repository}
}

func (s *Server) writeCurrencySettings(w http.ResponseWriter, r *http.Request, cfg virtualcurrency.Settings) {
	writeJSON(w, 200, map[string]any{"currency_key": currencyKey(r), "settings": currencySettingsView(cfg)})
}
func currencyAmount(amount int64, precision int) string {
	return virtualcurrency.FormatAmount(amount, precision)
}
func parseCurrencyAmount(value string, precision int) (int64, error) {
	return virtualcurrency.ParseAmount(value, precision)
}
func parseCurrencyDelta(value string, precision int) (int64, error) {
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	amount, err := parseCurrencyAmount(value, precision)
	if err != nil {
		return 0, err
	}
	if negative {
		return -amount, nil
	}
	return amount, nil
}
func currencyID(value string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 || strconv.FormatInt(n, 10) != value {
		return 0, virtualcurrency.ErrInvalid
	}
	return n, nil
}
func newCurrencyRequestID() (string, error) {
	var token [20]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// The general decoder rejects unknown/duplicate fields. Currency additionally
// rejects case aliases and nulls, avoiding JSON's implicit zero-value coercion.
func (s *Server) decodeCurrencyJSON(w http.ResponseWriter, r *http.Request, target any) ([]byte, bool) {
	if len(r.Header.Values("Content-Type")) != 1 {
		s.writeAPIError(w, r, 415, "request.json_content_type_required", "请求必须使用唯一的 application/json 内容类型", nil)
		return nil, false
	}
	body, ok := s.decodeStrictAPIJSONLimit(w, r, target, 8<<10, "8 KiB")
	if !ok {
		return nil, false
	}
	var value any
	if !utf8.Valid(body) || json.Unmarshal(body, &value) != nil || !canonicalCurrencyJSON(value) {
		s.writeAPIError(w, r, 400, "request.invalid_json", "字段名称、空值或文本编码无效", nil)
		return nil, false
	}
	return body, true
}
func canonicalCurrencyJSON(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case map[string]any:
		for key, item := range value {
			if key != strings.ToLower(key) || !canonicalCurrencyJSON(item) {
				return false
			}
		}
	case []any:
		for _, item := range value {
			if !canonicalCurrencyJSON(item) {
				return false
			}
		}
	}
	return true
}

func (s *Server) currencyPrincipal(w http.ResponseWriter, r *http.Request, write bool) (auth.Principal, bool) {
	principal, ok := s.socialPrincipal(w, r, write)
	if !ok {
		return principal, false
	}
	if write && (len(r.Header.Values("X-CSRF-Token")) != 1 || !s.currencySameOrigin(r)) {
		s.writeAPIError(w, r, 403, "auth.csrf_invalid", "请刷新登录状态后重试", nil)
		return principal, false
	}
	return principal, true
}

// Currency writes require a single canonical browser Origin, in addition to
// the authenticated session and CSRF token. Do not weaken other route contracts.
func (s *Server) currencySameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) != 1 || len(r.Header.Values("Sec-Fetch-Site")) > 1 {
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return false
	}
	return s.sameOriginRequest(r)
}

func (s *Server) currencyAdminWrite(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return principal, false
	}
	if !s.currencySameOrigin(r) {
		s.writeAPIError(w, r, 403, "auth.cross_site_request_rejected", "请从本站页面提交货币操作", nil)
		return principal, false
	}
	return principal, true
}
func (s *Server) currencyRate(w http.ResponseWriter, r *http.Request, key string, limit int) bool {
	allowed, retry := s.accessRate.allow(key, limit, time.Now())
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Seconds())+1)))
		s.writeAPIError(w, r, 429, "currency.rate_limited", "操作过于频繁，请稍后再试", nil)
	}
	return allowed
}

type currencySettingsDTO struct {
	Version               string `json:"version"`
	Enabled               bool   `json:"enabled"`
	Name                  string `json:"name"`
	Symbol                string `json:"symbol"`
	Precision             int    `json:"precision"`
	PrecisionLocked       bool   `json:"precision_locked"`
	MaxBalance            string `json:"max_balance"`
	AllowTransfers        bool   `json:"allow_transfers"`
	MinTransfer           string `json:"min_transfer"`
	MaxTransfer           string `json:"max_transfer"`
	DailyTransferLimit    string `json:"daily_transfer_limit"`
	CheckInEnabled        bool   `json:"check_in_enabled"`
	CheckInReward         string `json:"check_in_reward"`
	CheckInMode           string `json:"check_in_mode"`
	CheckInTimezone       string `json:"check_in_timezone"`
	CheckInIntervalHours  int    `json:"check_in_interval_hours"`
	RegistrationEnabled   bool   `json:"registration_enabled"`
	RegistrationReward    string `json:"registration_reward"`
	RegistrationSince     string `json:"registration_since"`
	PublicationEnabled    bool   `json:"publication_enabled"`
	PublicationReward     string `json:"publication_reward"`
	PublicationSince      string `json:"publication_since"`
	PublicationMinLength  int    `json:"publication_min_length"`
	PublicationDailyLimit int    `json:"publication_daily_limit"`
	RequireVerifiedEmail  bool   `json:"require_verified_email"`
	DailyRewardLimit      string `json:"daily_reward_limit"`
	DailyIssuanceLimit    string `json:"daily_issuance_limit"`
}

func currencySettingsView(v virtualcurrency.Settings) currencySettingsDTO {
	date := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	return currencySettingsDTO{Version: strconv.FormatInt(v.Version, 10), Enabled: v.Enabled, Name: v.Name, Symbol: v.Symbol, Precision: v.Precision, PrecisionLocked: v.PrecisionLocked,
		MaxBalance: currencyAmount(v.MaxBalance, v.Precision), AllowTransfers: v.AllowTransfers, MinTransfer: currencyAmount(v.MinTransfer, v.Precision), MaxTransfer: currencyAmount(v.MaxTransfer, v.Precision), DailyTransferLimit: currencyAmount(v.DailyTransferLimit, v.Precision),
		CheckInMode: v.CheckInRule().Mode, CheckInTimezone: v.CheckInRule().Timezone, CheckInEnabled: v.CheckInEnabled, CheckInReward: currencyAmount(v.CheckInReward, v.Precision), CheckInIntervalHours: v.CheckInIntervalHours,
		RegistrationEnabled: v.RegistrationEnabled, RegistrationReward: currencyAmount(v.RegistrationReward, v.Precision), RegistrationSince: date(v.RegistrationSince),
		PublicationEnabled: v.PublicationEnabled, PublicationReward: currencyAmount(v.PublicationReward, v.Precision), PublicationSince: date(v.PublicationSince), PublicationMinLength: v.PublicationMinLength, PublicationDailyLimit: v.PublicationDailyLimit,
		RequireVerifiedEmail: v.RequireVerifiedEmail, DailyRewardLimit: currencyAmount(v.DailyRewardLimit, v.Precision), DailyIssuanceLimit: currencyAmount(v.DailyIssuanceLimit, v.Precision)}
}
func (v currencySettingsDTO) settings() (virtualcurrency.Settings, error) {
	version, err := currencyID(v.Version)
	if err != nil {
		return virtualcurrency.Settings{}, err
	}
	value := virtualcurrency.Settings{Version: version, Enabled: v.Enabled, Name: v.Name, Symbol: v.Symbol, Precision: v.Precision, AllowTransfers: v.AllowTransfers,
		CheckInMode: v.CheckInMode, CheckInTimezone: v.CheckInTimezone, CheckInEnabled: v.CheckInEnabled, CheckInIntervalHours: v.CheckInIntervalHours, RegistrationEnabled: v.RegistrationEnabled, PublicationEnabled: v.PublicationEnabled,
		PublicationMinLength: v.PublicationMinLength, PublicationDailyLimit: v.PublicationDailyLimit, RequireVerifiedEmail: v.RequireVerifiedEmail}
	for _, field := range []struct {
		raw    string
		target *int64
	}{
		{v.MaxBalance, &value.MaxBalance}, {v.MinTransfer, &value.MinTransfer}, {v.MaxTransfer, &value.MaxTransfer}, {v.DailyTransferLimit, &value.DailyTransferLimit},
		{v.CheckInReward, &value.CheckInReward}, {v.RegistrationReward, &value.RegistrationReward}, {v.PublicationReward, &value.PublicationReward}, {v.DailyRewardLimit, &value.DailyRewardLimit}, {v.DailyIssuanceLimit, &value.DailyIssuanceLimit},
	} {
		if *field.target, err = parseCurrencyAmount(field.raw, v.Precision); err != nil {
			return virtualcurrency.Settings{}, err
		}
	}
	return virtualcurrency.NormalizeSettings(value)
}

type currencyHistoryItem struct {
	CurrencyKey string    `json:"currency_key,omitempty"`
	ID          string    `json:"id"`
	Delta       string    `json:"delta"`
	Balance     string    `json:"balance"`
	Kind        string    `json:"kind"`
	Label       string    `json:"label"`
	Incoming    bool      `json:"incoming"`
	Reason      string    `json:"reason,omitempty"`
	Peer        string    `json:"peer,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func currencyEntryView(e virtualcurrency.Entry, precision int) currencyHistoryItem {
	labels := map[string]string{"admin_credit": "管理员发放", "admin_debit": "管理员扣减", "credit": "系统发放", "transfer_out": "转出", "transfer_in": "转入", virtualcurrency.RewardCheckIn: "签到奖励", virtualcurrency.RewardRegistration: "注册奖励", virtualcurrency.RewardPublication: "发文奖励", virtualcurrency.PurchasePayment: "会员消费", virtualcurrency.PurchaseCanceled: "订单取消"}
	return currencyHistoryItem{ID: strconv.FormatInt(e.ID, 10), Delta: currencyAmount(e.Delta, precision), Balance: currencyAmount(e.Balance, precision), Kind: e.Kind, Label: labels[e.Kind], Incoming: e.Delta > 0, Reason: e.Reason, CreatedAt: e.CreatedAt}
}

func (s *Server) socialWalletRecipient(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyPrincipal(w, r, false)
	if !ok || s.currencyUnavailable(w, r) {
		return
	}
	if !s.currencyRate(w, r, "currency-recipient:"+strconv.FormatInt(p.UserID, 10), 60) {
		return
	}
	query, ok := s.strictAPIQuery(w, r, map[string]bool{"username": true})
	if !ok {
		return
	}
	user, err := s.auth.FindUserByUsername(r.Context(), strings.TrimSpace(query.Get("username")))
	if err != nil || user.Status != auth.UserStatusActive || user.ID == p.UserID {
		s.writeAPIError(w, r, 422, "currency.recipient_unavailable", "收款账号不可用，或与当前账号相同", nil)
		return
	}
	writeJSON(w, 200, map[string]string{"currency_key": currencyKey(r), "id": strconv.FormatInt(user.ID, 10), "username": user.Username, "display_name": user.DisplayName})
}

type currencyWalletDTO struct {
	CurrencyKey          string                `json:"currency_key"`
	Settings             currencySettingsDTO   `json:"settings"`
	UserID               string                `json:"user_id"`
	Username             string                `json:"username"`
	DisplayName          string                `json:"display_name"`
	Status               string                `json:"status"`
	Balance              string                `json:"balance"`
	TransferredToday     string                `json:"transferred_today"`
	TransferRemaining    string                `json:"transfer_remaining"`
	RewardedToday        string                `json:"rewarded_today"`
	RewardRemaining      string                `json:"reward_remaining"`
	VerificationRequired bool                  `json:"verification_required"`
	NextCheckInAt        *time.Time            `json:"next_check_in_at,omitempty"`
	CheckInReady         bool                  `json:"check_in_ready"`
	Entries              []currencyHistoryItem `json:"entries"`
	NextBefore           string                `json:"next_before"`
}

func (s *Server) currencyWalletView(r *http.Request, user auth.User, before int64) (currencyWalletDTO, error) {
	snapshot, err := s.currencyStore(r).Wallet(r.Context(), user.ID, before, 31)
	if err != nil {
		return currencyWalletDTO{}, err
	}
	cfg := snapshot.Settings
	value := currencyWalletDTO{CurrencyKey: currencyKey(r), Settings: currencySettingsView(cfg), UserID: strconv.FormatInt(user.ID, 10), Username: user.Username, DisplayName: user.DisplayName, Status: user.Status,
		Balance: currencyAmount(snapshot.Balance, cfg.Precision), TransferredToday: currencyAmount(snapshot.TransferredToday, cfg.Precision), TransferRemaining: currencyAmount(max(0, cfg.DailyTransferLimit-snapshot.TransferredToday), cfg.Precision),
		RewardedToday: currencyAmount(snapshot.RewardedToday, cfg.Precision), RewardRemaining: currencyAmount(max(0, cfg.DailyRewardLimit-snapshot.RewardedToday), cfg.Precision),
		VerificationRequired: cfg.RequireVerifiedEmail && user.EmailVerifiedAt == nil, Entries: []currencyHistoryItem{}}
	value.CheckInReady = cfg.Enabled && cfg.CheckInEnabled && !value.VerificationRequired && user.Status == auth.UserStatusActive && !time.Now().Before(snapshot.NextCheckInAt)
	if !snapshot.NextCheckInAt.IsZero() {
		at := snapshot.NextCheckInAt
		value.NextCheckInAt = &at
	}
	if len(snapshot.Entries) > 30 {
		snapshot.Entries = snapshot.Entries[:30]
		value.NextBefore = strconv.FormatInt(snapshot.Entries[29].ID, 10)
	}
	peers := map[int64]string{}
	for _, entry := range snapshot.Entries {
		row := currencyEntryView(entry, cfg.Precision)
		if entry.PeerID > 0 {
			if name, ok := peers[entry.PeerID]; ok {
				row.Peer = name
			} else {
				row.Peer = "已停用账号"
				if peer, e := s.auth.GetUser(r.Context(), entry.PeerID); e == nil && peer.Status == auth.UserStatusActive {
					row.Peer = peer.Username
				}
				peers[entry.PeerID] = row.Peer
			}
		}
		value.Entries = append(value.Entries, row)
	}
	return value, nil
}

func (s *Server) socialWallet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyPrincipal(w, r, false)
	if !ok || s.currencyUnavailable(w, r) {
		return
	}
	query, ok := s.strictAPIQuery(w, r, map[string]bool{"before": true})
	if !ok {
		return
	}
	var before int64
	var err error
	if raw := query.Get("before"); raw != "" {
		before, err = currencyID(raw)
		if err != nil {
			s.currencyError(w, r, err)
			return
		}
	}
	user, err := s.auth.GetUser(r.Context(), p.UserID)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	wallet, err := s.currencyWalletView(r, user, before)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	writeJSON(w, 200, wallet)
}

func (s *Server) socialWalletTransfer(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyPrincipal(w, r, true)
	if !ok || s.currencyUnavailable(w, r) {
		return
	}
	if !s.currencyRate(w, r, "currency-transfer:"+strconv.FormatInt(p.UserID, 10), 12) {
		return
	}
	var input struct {
		RecipientID string `json:"recipient_id"`
		Amount      string `json:"amount"`
		Version     string `json:"version"`
		RequestID   string `json:"request_id"`
	}
	if _, ok = s.decodeCurrencyJSON(w, r, &input); !ok {
		return
	}
	version, err := currencyID(input.Version)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	cfg, err := s.currencyStore(r).Settings(r.Context())
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	amount, err := parseCurrencyAmount(input.Amount, cfg.Precision)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	recipientID, err := currencyID(input.RecipientID)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	prior, fp, err := s.currencyStore(r).Receipt(r.Context(), p.UserID, input.RequestID)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	if len(prior) > 0 {
		if len(prior) != 2 || prior[0].Kind != "transfer_out" || fp != virtualcurrency.TransferFingerprint(recipientID, amount) {
			s.currencyError(w, r, virtualcurrency.ErrIdempotencyConflict)
			return
		}
		writeJSON(w, 200, map[string]any{"currency_key": currencyKey(r), "entry": currencyEntryView(prior[0], cfg.Precision), "recipient_id": input.RecipientID, "replayed": true})
		return
	}
	recipient, err := s.auth.GetUser(r.Context(), recipientID)
	if err != nil || recipient.Status != auth.UserStatusActive {
		s.writeAPIError(w, r, 422, "currency.recipient_unavailable", "收款账号不可用", nil)
		return
	}
	result, err := s.currencyStore(r).Transfer(r.Context(), virtualcurrency.TransferInput{UserID: p.UserID, RecipientID: recipient.ID, Amount: amount, ExpectedVersion: version, RequestID: input.RequestID})
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	// Only the sender's receipt is public; never expose the recipient's balance.
	writeJSON(w, 200, map[string]any{"currency_key": currencyKey(r), "entry": currencyEntryView(result.Sent, cfg.Precision), "recipient_id": input.RecipientID, "replayed": result.Replayed})
}

func (s *Server) socialWalletCheckIn(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyPrincipal(w, r, true)
	if !ok || s.currencyUnavailable(w, r) {
		return
	}
	if !s.currencyRate(w, r, "currency-check-in:"+strconv.FormatInt(p.UserID, 10), 12) {
		return
	}
	var input struct {
		Version   string `json:"version"`
		RequestID string `json:"request_id"`
	}
	if _, ok = s.decodeCurrencyJSON(w, r, &input); !ok {
		return
	}
	version, err := currencyID(input.Version)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	result, err := s.currencyRewardService(r).CheckIn(r.Context(), p.UserID, version, input.RequestID)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	cfg, err := s.currencyStore(r).Settings(r.Context())
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"currency_key": currencyKey(r), "entry": currencyEntryView(result.Entry, cfg.Precision), "replayed": result.Replayed})
}
func (s *Server) socialWalletSync(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyPrincipal(w, r, true)
	if !ok || s.currencyUnavailable(w, r) {
		return
	}
	if !s.currencyRate(w, r, "currency-sync:"+strconv.FormatInt(p.UserID, 10), 12) {
		return
	}
	if _, ok = s.decodeCurrencyJSON(w, r, &struct{}{}); !ok {
		return
	}
	result, err := s.currencyRewardService(r).Registration(r.Context(), p.UserID)
	if virtualcurrency.RewardPolicyError(err) {
		writeJSON(w, 200, map[string]any{"currency_key": currencyKey(r), "awarded": false})
		return
	}
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"currency_key": currencyKey(r), "awarded": !result.Replayed})
}

func currencyErrorInfo(err error) (int, string, string) {
	switch {
	case errors.Is(err, virtualcurrency.ErrCurrencyNotFound):
		return 404, "currency.not_found", "这个货币不存在，请重新选择"
	case errors.Is(err, virtualcurrency.ErrCurrencyExists):
		return 409, "currency.key_exists", "货币标识已经使用，请选择新的标识"
	case errors.Is(err, virtualcurrency.ErrCurrencyLimit):
		return 409, "currency.catalog_limit", "最多可配置 32 种货币，请管理已有货币"
	case errors.Is(err, virtualcurrency.ErrInvalid):
		return 422, "currency.invalid", "请检查规则、金额和请求编号；金额须使用普通十进制格式"
	case errors.Is(err, virtualcurrency.ErrDisabled):
		return 409, "currency.disabled", "虚拟币系统当前已关闭"
	case errors.Is(err, virtualcurrency.ErrConflict):
		return 409, "currency.settings_conflict", "规则已在其他位置更新，请重新载入后核对金额"
	case errors.Is(err, virtualcurrency.ErrPrecisionLocked):
		return 409, "currency.precision_locked", "已有流水，不能修改小数位数"
	case errors.Is(err, virtualcurrency.ErrInsufficient):
		return 409, "currency.insufficient", "余额不足"
	case errors.Is(err, virtualcurrency.ErrBalanceLimit):
		return 409, "currency.balance_limit", "操作会超过余额上限"
	case errors.Is(err, virtualcurrency.ErrTransferDisabled):
		return 403, "currency.transfer_disabled", "当前不允许用户之间转账"
	case errors.Is(err, virtualcurrency.ErrRateLimit):
		return 429, "currency.daily_limit", "已达到滚动 24 小时转账额度"
	case errors.Is(err, virtualcurrency.ErrRewardLimit):
		return 429, "currency.reward_limit", "已达到奖励额度、发文次数或站点发行预算"
	case errors.Is(err, virtualcurrency.ErrRewardIneligible):
		return 403, "currency.reward_ineligible", "暂不满足奖励规则，请检查开启状态和邮箱验证要求"
	case errors.Is(err, virtualcurrency.ErrIdempotencyConflict):
		return 409, "currency.request_reused", "请求编号已用于其他操作，请先核对历史流水"
	default:
		return 503, "currency.unavailable", "服务暂不可用，请保留当前请求后重试"
	}
}
func (s *Server) currencyError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := currencyErrorInfo(err)
	s.writeAPIError(w, r, status, code, message, nil)
}
