package httpserver

import (
	"encoding/json"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/virtualcurrency"
)

type currencyAdminView struct {
	Key             string
	Currencies      []currencyCatalogItem
	Modules         []currencyModuleView
	CreateRequestID string
	Settings        currencySettingsDTO
	RequestID       string
	Wallet          *currencyWalletDTO
	Username        string
	Notice          string
}

func (s *Server) adminCurrencyRead(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok || s.currencyUnavailable(w, r) {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	cfg, err := s.currencyStore(r).Settings(r.Context())
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	s.writeCurrencySettings(w, r, cfg)
}
func (s *Server) adminCurrencyUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyAdminWrite(w, r)
	if !ok || s.currencyUnavailable(w, r) {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		Settings currencySettingsDTO `json:"settings"`
	}
	raw, ok := s.decodeCurrencyJSON(w, r, &input)
	if !ok {
		return
	}
	var fields struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if json.Unmarshal(raw, &fields) != nil {
		s.currencyError(w, r, virtualcurrency.ErrInvalid)
		return
	}
	for _, key := range []string{"enabled", "allow_transfers", "precision", "check_in_enabled", "registration_enabled", "publication_enabled", "require_verified_email"} {
		if _, exists := fields.Settings[key]; !exists {
			s.writeAPIError(w, r, 400, "request.invalid_json", "请提交完整的货币规则", nil)
			return
		}
	}
	value, err := input.Settings.settings()
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	value, err = s.currencyStore(r).UpdateSettings(r.Context(), value.Version, value)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	slog.Info("currency settings updated", "actor_id", p.UserID, "currency_key", currencyKey(r), "version", value.Version)
	s.writeCurrencySettings(w, r, value)
}
func (s *Server) adminCurrencyWallet(w http.ResponseWriter, r *http.Request) {
	_, readOnly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage)
	if !ok || s.currencyUnavailable(w, r) {
		return
	}
	if readOnly {
		s.writeAPIError(w, r, 403, "auth.authentication_required", "请登录后查看账号信息", nil)
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"username": true, "before": true})
	if !ok {
		return
	}
	user, err := s.auth.FindUserByUsername(r.Context(), strings.TrimSpace(values.Get("username")))
	if err != nil {
		s.writeAPIError(w, r, 404, "currency.user_not_found", "账号不存在", nil)
		return
	}
	var before int64
	if raw := values.Get("before"); raw != "" {
		before, err = currencyID(raw)
		if err != nil {
			s.currencyError(w, r, err)
			return
		}
	}
	value, err := s.currencyWalletView(r, user, before)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	writeJSON(w, 200, value)
}
func (s *Server) adminCurrencyAdjust(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyAdminWrite(w, r)
	if !ok || s.currencyUnavailable(w, r) {
		return
	}
	if !s.currencyRate(w, r, "currency-adjust:"+strconv.FormatInt(p.UserID, 10), 60) {
		return
	}
	var input struct {
		UserID    string `json:"user_id"`
		Delta     string `json:"delta"`
		Reason    string `json:"reason"`
		RequestID string `json:"request_id"`
		Version   string `json:"version"`
	}
	if _, ok = s.decodeCurrencyJSON(w, r, &input); !ok {
		return
	}
	userID, err := currencyID(input.UserID)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	version, err := currencyID(input.Version)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	if _, err = s.auth.GetUser(r.Context(), userID); err != nil {
		s.writeAPIError(w, r, 404, "currency.user_not_found", "账号不存在", nil)
		return
	}
	cfg, err := s.currencyStore(r).Settings(r.Context())
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	delta, err := parseCurrencyDelta(input.Delta, cfg.Precision)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	entry, err := s.currencyStore(r).Adjust(r.Context(), virtualcurrency.Adjustment{ActorID: p.UserID, UserID: userID, Delta: delta, ExpectedVersion: version, RequestID: input.RequestID, Reason: input.Reason})
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	view := currencyEntryView(entry, cfg.Precision)
	view.CurrencyKey = currencyKey(r)
	writeJSON(w, 201, view)
}

func (s *Server) adminCurrencyPage(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if s.currency == nil && s.currencyCatalog == nil {
		s.renderFeatureRecovery(w, r, "currency", "", s.adminRecoveryIdentity("currency", p, readOnly), 503, "")
		return
	}
	if s.currencyUnavailable(w, r) {
		return
	}
	query, ok := s.strictAPIQuery(w, r, map[string]bool{"currency": true, "notice": true, "username": true, "before": true})
	if !ok {
		return
	}
	cfg, err := s.currencyStore(r).Settings(r.Context())
	if err != nil {
		s.renderFeatureRecovery(w, r, "currency", currencyKey(r), s.adminRecoveryIdentity("currency", p, readOnly), 503, "钱包规则暂时不可读，请检查服务状态后重新检测。")
		return
	}
	requestID, err := newCurrencyRequestID()
	if err != nil {
		s.renderError(w, 503, "暂时无法创建操作编号")
		return
	}
	data := s.baseViewFor(w, "虚拟币与奖励", "管理虚拟币规则、自动奖励与账号流水", "/admin/currency/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-currency"
	data.Admin = s.adminView("currency", p, readOnly)
	items, err := s.currencyCatalogView(r)
	if err != nil {
		s.renderError(w, 503, "货币列表暂不可用")
		return
	}
	createID, err := newCurrencyRequestID()
	if err != nil {
		s.renderError(w, 503, "暂时无法创建操作编号")
		return
	}
	data.Currency = currencyAdminView{Key: currencyKey(r), Currencies: items, Modules: currencyModulesView(cfg), CreateRequestID: createID, Settings: currencySettingsView(cfg), RequestID: requestID, Username: query.Get("username")}
	switch query.Get("notice") {
	case "saved":
		data.Currency.Notice = "规则已保存。后续操作将使用新规则。"
	case "adjusted":
		data.Currency.Notice = "余额调整已记入流水。"
	case "created":
		data.Currency.Notice = "货币已创建，默认关闭。配置各模块后即可启用。"
	}
	if data.Currency.Username != "" && !readOnly {
		user, e := s.auth.FindUserByUsername(r.Context(), strings.TrimSpace(data.Currency.Username))
		if e != nil {
			data.Currency.Notice = "没有找到这个账号，请输入完整用户名。"
		} else {
			var before int64
			if raw := query.Get("before"); raw != "" {
				before, e = currencyID(raw)
				if e != nil {
					s.renderError(w, 400, "流水位置无效")
					return
				}
			}
			wallet, e := s.currencyWalletView(r, user, before)
			if e != nil {
				s.renderError(w, 503, "账号数据暂不可用")
				return
			}
			data.Currency.Wallet = &wallet
		}
	}
	s.render(w, 200, "admin-currency", data)
}

func currencyFormSettings(form url.Values) (currencySettingsDTO, error) {
	value := currencySettingsDTO{Version: form.Get("version"), Name: form.Get("name"), Symbol: form.Get("symbol"), MaxBalance: form.Get("max_balance"),
		MinTransfer: form.Get("min_transfer"), MaxTransfer: form.Get("max_transfer"), DailyTransferLimit: form.Get("daily_transfer_limit"),
		CheckInMode: form.Get("check_in_mode"), CheckInTimezone: form.Get("check_in_timezone"), CheckInReward: form.Get("check_in_reward"), RegistrationReward: form.Get("registration_reward"), PublicationReward: form.Get("publication_reward"), DailyRewardLimit: form.Get("daily_reward_limit"), DailyIssuanceLimit: form.Get("daily_issuance_limit")}
	for _, field := range []struct {
		key    string
		target *bool
	}{
		{"enabled", &value.Enabled}, {"allow_transfers", &value.AllowTransfers}, {"check_in_enabled", &value.CheckInEnabled}, {"registration_enabled", &value.RegistrationEnabled}, {"publication_enabled", &value.PublicationEnabled}, {"require_verified_email", &value.RequireVerifiedEmail},
	} {
		switch form.Get(field.key) {
		case "on":
			*field.target = true
		case "", "off":
			*field.target = false
		default:
			return value, virtualcurrency.ErrInvalid
		}
	}
	for _, field := range []struct {
		key    string
		target *int
	}{
		{"precision", &value.Precision}, {"check_in_interval_hours", &value.CheckInIntervalHours}, {"publication_min_length", &value.PublicationMinLength}, {"publication_daily_limit", &value.PublicationDailyLimit},
	} {
		n, err := strconv.ParseInt(form.Get(field.key), 10, 32)
		if err != nil {
			return value, virtualcurrency.ErrInvalid
		}
		*field.target = int(n)
	}
	return value, nil
}
func (s *Server) adminCurrencyForm(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if readOnly || !s.auth.Configured() || !s.currencySameOrigin(r) {
		http.Error(w, "无权提交此表单", 403)
		return
	}
	if s.currencyUnavailable(w, r) {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{"currency": true}); !ok {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" || len(r.Header.Values("Content-Type")) != 1 {
		http.Error(w, "表单格式不支持", 415)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err = r.ParseForm(); err != nil {
		http.Error(w, "表单过大或格式无效", 400)
		return
	}
	form := r.PostForm
	allowed := map[string]bool{"csrf_token": true, "action": true, "version": true}
	switch form.Get("action") {
	case "settings":
		allowed["module"] = true
		for _, key := range []string{"enabled", "name", "symbol", "precision", "max_balance", "allow_transfers", "min_transfer", "max_transfer", "daily_transfer_limit", "check_in_enabled", "check_in_reward", "check_in_mode", "check_in_timezone", "check_in_interval_hours", "registration_enabled", "registration_reward", "publication_enabled", "publication_reward", "publication_min_length", "publication_daily_limit", "require_verified_email", "daily_reward_limit", "daily_issuance_limit"} {
			allowed[key] = true
		}
	case "create":
		for _, key := range []string{"key", "name", "symbol", "precision", "request_id"} {
			allowed[key] = true
		}
	case "adjust":
		for _, key := range []string{"user_id", "delta", "reason", "request_id"} {
			allowed[key] = true
		}
	default:
		http.Error(w, "操作无效", 400)
		return
	}
	for key, values := range form {
		if !allowed[key] || len(values) != 1 {
			http.Error(w, "表单字段未知或重复", 400)
			return
		}
	}
	if !s.auth.CheckCSRF(p, form.Get("csrf_token")) {
		http.Error(w, "登录状态已变化，请刷新后重试", 403)
		return
	}
	fail := func(err error) { status, _, message := currencyErrorInfo(err); http.Error(w, message, status) }
	if form.Get("action") == "create" {
		if !s.currencyRate(w, r, "currency-create:"+strconv.FormatInt(p.UserID, 10), 12) {
			return
		}
		precision, err := strconv.Atoi(form.Get("precision"))
		if err != nil {
			fail(virtualcurrency.ErrInvalid)
			return
		}
		item, _, err := s.currencyCatalog.Create(r.Context(), virtualcurrency.CreateCurrency{Key: form.Get("key"), Name: form.Get("name"), Symbol: form.Get("symbol"), Precision: precision, ActorID: p.UserID, RequestID: form.Get("request_id")})
		if err != nil {
			fail(err)
			return
		}
		http.Redirect(w, r, "/admin/currency/?currency="+url.QueryEscape(item.Key)+"&notice=created", http.StatusSeeOther)
		return
	}
	if form.Get("action") == "settings" {
		if key := form.Get("module"); key != "" {
			module, ok := currencyModule(key)
			if !ok {
				fail(virtualcurrency.ErrInvalid)
				return
			}
			current, err := s.currencyStore(r).Settings(r.Context())
			if err != nil {
				fail(err)
				return
			}
			values, err := currencyFormModuleValues(form, module)
			if err != nil {
				fail(err)
				return
			}
			value, err := mergeCurrencyModule(current, module, form.Get("version"), values)
			if err != nil {
				fail(err)
				return
			}
			value, err = s.currencyStore(r).UpdateSettings(r.Context(), current.Version, value)
			if err != nil {
				fail(err)
				return
			}
			slog.Info("currency module updated", "actor_id", p.UserID, "currency_key", currencyKey(r), "module", module.Key, "version", value.Version)
			http.Redirect(w, r, "/admin/currency/?currency="+url.QueryEscape(currencyKey(r))+"&notice=saved", http.StatusSeeOther)
			return
		}
		dto, err := currencyFormSettings(form)
		if err != nil {
			fail(err)
			return
		}
		value, err := dto.settings()
		if err != nil {
			fail(err)
			return
		}
		value, err = s.currencyStore(r).UpdateSettings(r.Context(), value.Version, value)
		if err != nil {
			fail(err)
			return
		}
		slog.Info("currency settings updated", "actor_id", p.UserID, "currency_key", currencyKey(r), "version", value.Version)
		http.Redirect(w, r, "/admin/currency/?currency="+url.QueryEscape(currencyKey(r))+"&notice=saved", http.StatusSeeOther)
		return
	}
	if !s.currencyRate(w, r, "currency-adjust:"+strconv.FormatInt(p.UserID, 10), 60) {
		return
	}
	userID, err := currencyID(form.Get("user_id"))
	if err != nil {
		fail(err)
		return
	}
	user, err := s.auth.GetUser(r.Context(), userID)
	if err != nil {
		http.Error(w, "账号不存在", 404)
		return
	}
	version, err := currencyID(form.Get("version"))
	if err != nil {
		fail(err)
		return
	}
	cfg, err := s.currencyStore(r).Settings(r.Context())
	if err != nil {
		fail(err)
		return
	}
	delta, err := parseCurrencyDelta(form.Get("delta"), cfg.Precision)
	if err != nil {
		fail(err)
		return
	}
	_, err = s.currencyStore(r).Adjust(r.Context(), virtualcurrency.Adjustment{ActorID: p.UserID, UserID: userID, Delta: delta, ExpectedVersion: version, RequestID: form.Get("request_id"), Reason: form.Get("reason")})
	if err != nil {
		fail(err)
		return
	}
	http.Redirect(w, r, "/admin/currency/?currency="+url.QueryEscape(currencyKey(r))+"&notice=adjusted&username="+url.QueryEscape(user.Username), http.StatusSeeOther)
}

func currencyFormModuleValues(form url.Values, module currencyModuleView) (map[string]json.RawMessage, error) {
	values := make(map[string]json.RawMessage, len(module.Fields))
	for _, field := range module.Fields {
		var value any = form.Get(field.Key)
		switch field.Kind {
		case "toggle":
			switch form.Get(field.Key) {
			case "on":
				value = true
			case "", "off":
				value = false
			default:
				return nil, virtualcurrency.ErrInvalid
			}
		case "integer":
			n, err := strconv.Atoi(form.Get(field.Key))
			if err != nil {
				return nil, virtualcurrency.ErrInvalid
			}
			value = n
		}
		values[field.Key], _ = json.Marshal(value)
	}
	return values, nil
}
