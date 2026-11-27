package httpserver

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"newpress.local/newpress/internal/virtualcurrency"
)

// One definition drives the rendered controls and the patch whitelist. Values
// and activation timestamps still pass through the ledger's atomic validation.
type currencyModuleField struct {
	Key, Label, Kind, Help string
	Min, Max               int
	Value                  string
	Checked, Locked        bool
}
type currencyModuleView struct {
	Key, Number, Title, Description string
	Fields                          []currencyModuleField
}

var currencyModuleDefinitions = []currencyModuleView{
	{Key: "identity", Title: "货币定义", Description: "设置这项货币的名称、金额格式与余额上限。", Fields: []currencyModuleField{
		{Key: "enabled", Label: "启用这项货币", Kind: "toggle", Help: "关闭后保留余额和流水，暂停转账与自动奖励。"},
		{Key: "name", Label: "货币名称", Kind: "text", Max: 40, Help: "例如：阅读积分、金币。"},
		{Key: "symbol", Label: "展示符号 / 单位", Kind: "text", Max: 12, Help: "例如：分、枚、G。"},
		{Key: "precision", Label: "小数位数", Kind: "integer", Min: 0, Max: 6, Help: "首次记账后锁定。修改时会同步显示其他模块的金额格式。"},
		{Key: "max_balance", Label: "单个账号余额上限", Kind: "amount", Help: "不能低于已有余额、转账上限或个人奖励上限。"},
	}},
	{Key: "transfers", Title: "用户转账", Description: "单独控制用户之间的转账和转出额度。", Fields: []currencyModuleField{
		{Key: "allow_transfers", Label: "允许用户之间转账", Kind: "toggle", Help: "用户核对收款账号与金额后转账。"},
		{Key: "min_transfer", Label: "单笔转账最低", Kind: "amount"},
		{Key: "max_transfer", Label: "单笔转账最高", Kind: "amount"},
		{Key: "daily_transfer_limit", Label: "每人 24 小时转出上限", Kind: "amount", Help: "使用滚动 24 小时额度，不能超过余额上限。"},
	}},
	{Key: "check-in", Title: "签到奖励", Description: "配置用户主动签到的奖励与领取间隔。", Fields: []currencyModuleField{
		{Key: "check_in_enabled", Label: "开启签到奖励", Kind: "toggle", Help: "用户从这项货币的钱包中签到领取。"},
		{Key: "check_in_reward", Label: "每次签到奖励", Kind: "amount"},
		{Key: "check_in_mode", Label: "签到周期", Kind: "schedule", Help: "自然天按指定时区零点重置；两种模式仍受滚动 24 小时奖励额度限制。"},
		{Key: "check_in_timezone", Label: "自然天时区", Kind: "text", Max: 100, Help: "北京时间：Asia/Shanghai；也可填写 UTC 或其他 IANA 时区。"},
		{Key: "check_in_interval_hours", Label: "领取间隔（小时）", Kind: "integer", Min: 1, Max: 720, Help: "24 表示两次领取至少间隔一天。"},
	}},
	{Key: "registration", Title: "注册奖励", Description: "符合资格的新账号自动获得奖励，每人一次。", Fields: []currencyModuleField{
		{Key: "registration_enabled", Label: "开启注册奖励", Kind: "toggle", Help: "仅限这项货币首次开启注册奖励后注册的账号。"},
		{Key: "registration_reward", Label: "每个新账号奖励", Kind: "amount"},
	}},
	{Key: "publication", Title: "发文奖励", Description: "按公开文章奖励创作，重复发布不会再次发奖。", Fields: []currencyModuleField{
		{Key: "publication_enabled", Label: "开启发文奖励", Kind: "toggle", Help: "仅限这项货币首次开启发文奖励后创建并公开的文章。"},
		{Key: "publication_reward", Label: "每篇文章奖励", Kind: "amount"},
		{Key: "publication_min_length", Label: "最低正文字数", Kind: "integer", Min: 1, Max: 100000, Help: "不计空白、隐藏字符和格式标记。"},
		{Key: "publication_daily_limit", Label: "每人 24 小时最多奖励（篇）", Kind: "integer", Min: 1, Max: 1000},
	}},
	{Key: "reward-policy", Title: "奖励资格与预算", Description: "统一管理这项货币的奖励资格、个人额度和全站预算。", Fields: []currencyModuleField{
		{Key: "require_verified_email", Label: "奖励需要已验证邮箱", Kind: "toggle", Help: "对签到、注册和发文奖励生效。"},
		{Key: "daily_reward_limit", Label: "每人 24 小时奖励上限", Kind: "amount", Help: "开启的各项奖励金额不能超过此上限。"},
		{Key: "daily_issuance_limit", Label: "全站 24 小时奖励预算", Kind: "amount", Help: "只统计这项货币的自动奖励；管理员调整单独记账。"},
	}},
}

func currencyModule(key string) (currencyModuleView, bool) {
	for _, module := range currencyModuleDefinitions {
		if module.Key == key {
			return module, true
		}
	}
	return currencyModuleView{}, false
}

func currencyModulesView(cfg virtualcurrency.Settings) []currencyModuleView {
	raw, _ := json.Marshal(currencySettingsView(cfg))
	var values map[string]json.RawMessage
	_ = json.Unmarshal(raw, &values)
	result := make([]currencyModuleView, 0, len(currencyModuleDefinitions))
	for i, definition := range currencyModuleDefinitions {
		module := definition
		module.Number = fmt.Sprintf("%02d", i+1)
		module.Fields = append([]currencyModuleField(nil), definition.Fields...)
		for j := range module.Fields {
			field := &module.Fields[j]
			switch field.Kind {
			case "toggle":
				_ = json.Unmarshal(values[field.Key], &field.Checked)
			case "integer":
				var n int
				_ = json.Unmarshal(values[field.Key], &n)
				field.Value = strconv.Itoa(n)
			default:
				_ = json.Unmarshal(values[field.Key], &field.Value)
			}
			if field.Key == "precision" && cfg.PrecisionLocked {
				field.Locked, field.Help = true, "已有流水，小数位数已锁定。"
			}
		}
		result = append(result, module)
	}
	return result
}

func mergeCurrencyModule(current virtualcurrency.Settings, module currencyModuleView, version string, values map[string]json.RawMessage) (virtualcurrency.Settings, error) {
	expected, err := currencyID(version)
	if err != nil || len(values) == 0 {
		return virtualcurrency.Settings{}, virtualcurrency.ErrInvalid
	}
	if current.Version != expected {
		return virtualcurrency.Settings{}, virtualcurrency.ErrConflict
	}
	allowed := make(map[string]bool, len(module.Fields))
	for _, field := range module.Fields {
		allowed[field.Key] = true
	}
	for key := range values {
		if !allowed[key] {
			return virtualcurrency.Settings{}, virtualcurrency.ErrInvalid
		}
	}
	// A precision edit cannot silently rescale amounts from other modules.
	// Render their existing integer units at the new precision before merging.
	if raw, ok := values["precision"]; ok {
		if json.Unmarshal(raw, &current.Precision) != nil || current.Precision < 0 || current.Precision > 6 {
			return virtualcurrency.Settings{}, virtualcurrency.ErrInvalid
		}
	}
	raw, err := json.Marshal(currencySettingsView(current))
	if err != nil {
		return virtualcurrency.Settings{}, err
	}
	var merged map[string]json.RawMessage
	if err = json.Unmarshal(raw, &merged); err != nil {
		return virtualcurrency.Settings{}, err
	}
	for key, value := range values {
		merged[key] = value
	}
	raw, err = json.Marshal(merged)
	if err != nil {
		return virtualcurrency.Settings{}, err
	}
	var dto currencySettingsDTO
	if json.Unmarshal(raw, &dto) != nil {
		return virtualcurrency.Settings{}, virtualcurrency.ErrInvalid
	}
	return dto.settings()
}

func (s *Server) adminCurrencyModuleUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.currencyAdminWrite(w, r)
	if !ok || s.currencyUnavailable(w, r) {
		return
	}
	module, ok := currencyModule(r.PathValue("module"))
	if !ok {
		s.writeAPIError(w, r, 404, "currency.module_not_found", "这个设置模块不存在", nil)
		return
	}
	var input struct {
		Version string                     `json:"version"`
		Values  map[string]json.RawMessage `json:"values"`
	}
	if _, ok = s.decodeCurrencyJSON(w, r, &input); !ok {
		return
	}
	current, err := s.currencyStore(r).Settings(r.Context())
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	value, err := mergeCurrencyModule(current, module, input.Version, input.Values)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	value, err = s.currencyStore(r).UpdateSettings(r.Context(), current.Version, value)
	if err != nil {
		s.currencyError(w, r, err)
		return
	}
	slog.Info("currency module updated", "actor_id", p.UserID, "currency_key", currencyKey(r), "module", module.Key, "version", value.Version)
	writeJSON(w, 200, map[string]any{"currency_key": currencyKey(r), "module": module.Key, "settings": currencySettingsView(value)})
}
