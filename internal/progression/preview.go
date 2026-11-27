package progression

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/virtualcurrency"
)

// SettingsIssue is an explainable rule failure or operational reminder. Only
// error issues block a save; warnings keep valid but surprising configurations
// visible to the administrator.
type SettingsIssue struct {
	Severity string `json:"severity"`
	Field    string `json:"field"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type SettingsMetric struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

type PreviewPlan struct {
	Index             int    `json:"index"`
	Key               string `json:"key"`
	Name              string `json:"name"`
	Currency          string `json:"currency"`
	CurrencyName      string `json:"currency_name"`
	Symbol            string `json:"symbol"`
	InputPrice        string `json:"input_price"`
	Price             string `json:"price"`
	Amount            string `json:"amount"`
	Precision         int    `json:"precision"`
	Days              int    `json:"days"`
	ExperiencePercent int    `json:"experience_percent"`
	Enabled           bool   `json:"enabled"`
	PurchaseEnabled   bool   `json:"purchase_enabled"`
	Status            string `json:"status"`
}

type PreviewLevel struct {
	Index     int    `json:"index"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	Threshold string `json:"threshold"`
	Gap       string `json:"gap"`
	Status    string `json:"status"`
}

type SettingsPreview struct {
	Valid   bool             `json:"valid"`
	Issues  []SettingsIssue  `json:"issues"`
	Metrics []SettingsMetric `json:"metrics"`
	Plans   []PreviewPlan    `json:"plans"`
	Levels  []PreviewLevel   `json:"levels"`
}

type CurrencyFacts struct {
	Key        string
	Name       string
	Symbol     string
	Precision  int
	Enabled    bool
	MaxBalance int64
}

type PreviewContext struct {
	Current           Settings
	MaximumExperience int64
	Currencies        []CurrencyFacts
	UsedPlans         map[string]bool
}

func validationIssue(out *[]SettingsIssue, field, code, message string) {
	*out = append(*out, SettingsIssue{Severity: "error", Field: field, Code: code, Message: message})
}

func previewWarning(out *[]SettingsIssue, field, code, message string) {
	*out = append(*out, SettingsIssue{Severity: "warning", Field: field, Code: code, Message: message})
}

// validationIssues is the single source of truth for persisted progression
// settings. The preview endpoint explains the same rules without weakening
// the authoritative save path.
func validationIssues(s Settings) []SettingsIssue {
	issues := []SettingsIssue{}
	if s.Version < 1 {
		validationIssue(&issues, "settings.version", "settings.version_invalid", "配置版本无效，请重新载入。")
	}
	if !textOK(s.Experience.Name, 1, 24) {
		validationIssue(&issues, "experience.name", "experience.name_invalid", "经验名称须为 1–24 个可见字符，且不能包含标签或控制字符。")
	}
	if s.Experience.Maximum < 1 {
		validationIssue(&issues, "experience.maximum", "experience.maximum_invalid", "账号经验上限必须大于 0。")
	}
	if s.Experience.DailyLimit < 1 {
		validationIssue(&issues, "experience.daily_limit", "experience.daily_limit_invalid", "近 24 小时奖励上限必须大于 0。")
	} else if s.Experience.Maximum > 0 && s.Experience.DailyLimit > s.Experience.Maximum {
		validationIssue(&issues, "experience.daily_limit", "experience.daily_limit_exceeds_maximum", "近 24 小时奖励上限不能高于账号经验上限。")
	}
	if s.CheckIn.Amount < 1 {
		validationIssue(&issues, "check_in.amount", "check_in.amount_invalid", "签到基础经验必须大于 0。")
	}
	if s.CheckIn.Rule().Validate() != nil {
		validationIssue(&issues, "check_in.mode", "check_in.rule_invalid", "签到模式或时区无效；请使用间隔模式或指定 IANA 时区的自然天模式。")
	}
	if s.CheckIn.IntervalHours < 1 || s.CheckIn.IntervalHours > 720 {
		validationIssue(&issues, "check_in.interval_hours", "check_in.interval_invalid", "签到间隔须在 1–720 小时。")
	}
	if s.Registration.Amount < 1 {
		validationIssue(&issues, "registration.amount", "registration.amount_invalid", "注册基础经验必须大于 0。")
	}
	if s.Publication.Amount < 1 {
		validationIssue(&issues, "publication.amount", "publication.amount_invalid", "发文基础经验必须大于 0。")
	}
	if s.Publication.MinLength < 1 || s.Publication.MinLength > 100_000 {
		validationIssue(&issues, "publication.min_length", "publication.min_length_invalid", "最少正文长度须在 1–100000 个可见字符。")
	}
	if s.Publication.DailyLimit < 1 || s.Publication.DailyLimit > 1000 {
		validationIssue(&issues, "publication.daily_limit", "publication.daily_limit_invalid", "每人近 24 小时发文奖励篇数须在 1–1000。")
	}
	if len(s.Levels.Items) < 1 || len(s.Levels.Items) > MaxLevels {
		validationIssue(&issues, "levels.items", "levels.count_invalid", fmt.Sprintf("等级数量须在 1–%d 之间。", MaxLevels))
	}
	keys := map[string]bool{}
	for i, level := range s.Levels.Items {
		field := fmt.Sprintf("levels.items[%d]", i)
		if !virtualcurrency.ValidCurrencyKey(level.Key) {
			validationIssue(&issues, field+".key", "levels.key_invalid", "等级标识须为 1–32 位小写字母、数字或连字符。")
		}
		if keys[level.Key] {
			validationIssue(&issues, field+".key", "levels.key_duplicate", "等级标识不能重复。")
		}
		keys[level.Key] = true
		if !textOK(level.Name, 1, 40) {
			validationIssue(&issues, field+".name", "levels.name_invalid", "等级名称须为 1–40 个可见字符。")
		}
		if !colorOK(level.Color) {
			validationIssue(&issues, field+".color", "levels.color_invalid", "等级徽章颜色须为六位十六进制颜色。")
		}
		if level.Threshold < 0 {
			validationIssue(&issues, field+".threshold", "levels.threshold_negative", "等级门槛不能为负数。")
		}
		if s.Experience.Maximum > 0 && level.Threshold > s.Experience.Maximum {
			validationIssue(&issues, field+".threshold", "levels.threshold_exceeds_maximum", "等级门槛不能高于账号经验上限。")
		}
		if i == 0 && level.Threshold != 0 {
			validationIssue(&issues, field+".threshold", "levels.first_threshold", "第一级门槛必须为 0。")
		}
		if i > 0 && level.Threshold <= s.Levels.Items[i-1].Threshold {
			validationIssue(&issues, field+".threshold", "levels.threshold_order", "等级门槛必须严格递增。")
		}
	}
	if len(s.Membership.Plans) < 1 || len(s.Membership.Plans) > MaxPlans {
		validationIssue(&issues, "membership.plans", "membership.count_invalid", fmt.Sprintf("会员方案数量须在 1–%d 之间。", MaxPlans))
	}
	keys = map[string]bool{}
	maximumPercent := 100
	for i, plan := range s.Membership.Plans {
		field := fmt.Sprintf("membership.plans[%d]", i)
		if plan.Tier < 0 || plan.Tier > 100 {
			validationIssue(&issues, field+".tier", "membership.tier_invalid", "会员级别须为 0–100；0 按普通会员一级处理。")
		}
		if !virtualcurrency.ValidCurrencyKey(plan.Key) {
			validationIssue(&issues, field+".key", "membership.key_invalid", "方案标识须为 1–32 位小写字母、数字或连字符。")
		}
		if keys[plan.Key] {
			validationIssue(&issues, field+".key", "membership.key_duplicate", "方案标识不能重复。")
		}
		keys[plan.Key] = true
		if !textOK(plan.Name, 1, 40) {
			validationIssue(&issues, field+".name", "membership.name_invalid", "方案名称须为 1–40 个可见字符。")
		}
		if !textOK(plan.Description, 0, 160) {
			validationIssue(&issues, field+".description", "membership.description_invalid", "方案简介最多 160 个可见字符。")
		}
		if len(plan.Benefits) > MaxPlanBenefits {
			validationIssue(&issues, field+".benefits", "membership.benefits_limit", fmt.Sprintf("每个方案最多填写 %d 项权益说明。", MaxPlanBenefits))
		}
		benefits := make(map[string]bool)
		for j, benefit := range plan.Benefits {
			benefitField := fmt.Sprintf("%s.benefits[%d]", field, j)
			if !textOK(benefit, 1, MaxPlanBenefitLength) {
				validationIssue(&issues, benefitField, "membership.benefit_invalid", fmt.Sprintf("每项权益须为 1–%d 个可见字符，不含首尾空格、换行或 HTML。", MaxPlanBenefitLength))
			}
			if benefits[benefit] {
				validationIssue(&issues, benefitField, "membership.benefit_duplicate", "同一方案的权益说明不能重复。")
			}
			benefits[benefit] = true
		}
		if !colorOK(plan.Color) {
			validationIssue(&issues, field+".color", "membership.color_invalid", "方案徽章颜色须为六位十六进制颜色。")
		}
		if plan.Days < 1 || plan.Days > MaxMembershipDays {
			validationIssue(&issues, field+".days", "membership.days_invalid", fmt.Sprintf("会员有效天数须在 1–%d。", MaxMembershipDays))
		}
		if plan.ExperiencePercent < 100 || plan.ExperiencePercent > 1000 {
			validationIssue(&issues, field+".experience_percent", "membership.percent_invalid", "经验倍率须在 100%–1000%。")
		}
		if !virtualcurrency.ValidCurrencyKey(plan.Currency) {
			validationIssue(&issues, field+".currency", "membership.currency_invalid", "购买币种标识无效。")
		}
		precision := 0
		if dot := strings.IndexByte(plan.Price, '.'); dot >= 0 {
			precision = len(plan.Price) - dot - 1
		}
		if precision > 6 {
			validationIssue(&issues, field+".price", "membership.price_precision", "购买价格最多支持 6 位小数。")
		} else if amount, err := virtualcurrency.ParseAmount(plan.Price, precision); err != nil || amount < 1 {
			validationIssue(&issues, field+".price", "membership.price_invalid", "购买价格必须为大于 0 的规范金额。")
		}
		if s.Membership.Enabled {
			maximumPercent = max(maximumPercent, plan.ExperiencePercent)
		}
	}
	for _, rule := range []struct {
		enabled bool
		amount  int64
		field   string
		label   string
	}{
		{s.CheckIn.Enabled, s.CheckIn.Amount, "check_in.amount", "签到"},
		{s.Registration.Enabled, s.Registration.Amount, "registration.amount", "注册"},
		{s.Publication.Enabled, s.Publication.Amount, "publication.amount", "发文"},
	} {
		if rule.enabled && (rule.amount > math.MaxInt64/int64(maximumPercent) || rule.amount*int64(maximumPercent)/100 > s.Experience.DailyLimit) {
			validationIssue(&issues, rule.field, "rewards.exceeds_daily_limit", fmt.Sprintf("%s经验按最高会员倍率发放后会超过近 24 小时奖励上限，请提高上限或降低单次奖励/倍率。", rule.label))
		}
	}
	return issues
}

func hasError(issues []SettingsIssue, field string) bool {
	for _, issue := range issues {
		if issue.Severity == "error" && (issue.Field == field || strings.HasPrefix(issue.Field, field+".")) {
			return true
		}
	}
	return false
}

func hasWarning(issues []SettingsIssue, field string) bool {
	for _, issue := range issues {
		if issue.Severity == "warning" && (issue.Field == field || strings.HasPrefix(issue.Field, field+".")) {
			return true
		}
	}
	return false
}

func currencyFact(currencies []CurrencyFacts, key string) (CurrencyFacts, bool) {
	for _, currency := range currencies {
		if currency.Key == key {
			return currency, true
		}
	}
	return CurrencyFacts{}, false
}

// AnalyzeSettings evaluates a complete draft in memory. It never calls a
// mutating store operation and never includes account identifiers.
func AnalyzeSettings(s Settings, ctx PreviewContext) SettingsPreview {
	issues := validationIssues(s)
	if ctx.Current.Version > 0 && s.Version != ctx.Current.Version {
		validationIssue(&issues, "settings.version", "settings.version_changed", "规则已由其他操作更新，请重新载入后再检查。")
	}
	if ctx.MaximumExperience > s.Experience.Maximum {
		validationIssue(&issues, "experience.maximum", "experience.maximum_below_existing", "已有账号经验高于新的上限；请先核对现有账号或提高上限。")
	}
	for _, old := range ctx.Current.Membership.Plans {
		if _, found := s.Plan(old.Key); !found && ctx.UsedPlans[old.Key] {
			validationIssue(&issues, "membership.plans", "membership.plan_used", fmt.Sprintf("方案「%s」已被使用，不能删除；可停用该方案。", old.Name))
		}
	}

	plans := make([]PreviewPlan, 0, len(s.Membership.Plans))
	for i, plan := range s.Membership.Plans {
		field := fmt.Sprintf("membership.plans[%d]", i)
		if plan.Tier < 0 || plan.Tier > 100 {
			validationIssue(&issues, field+".tier", "membership.tier_invalid", "会员级别须为 0–100；0 按普通会员一级处理。")
		}
		preview := PreviewPlan{Index: i, Key: plan.Key, Name: plan.Name, Currency: plan.Currency, InputPrice: plan.Price, Price: plan.Price, Precision: -1, Days: plan.Days, ExperiencePercent: plan.ExperiencePercent, Enabled: plan.Enabled, PurchaseEnabled: plan.PurchaseEnabled}
		if currency, ok := currencyFact(ctx.Currencies, plan.Currency); ok {
			preview.CurrencyName, preview.Symbol, preview.Precision = currency.Name, currency.Symbol, currency.Precision
			if amount, err := virtualcurrency.ParseAmount(plan.Price, currency.Precision); err == nil {
				preview.Amount = strconv.FormatInt(amount, 10)
				preview.Price = virtualcurrency.FormatAmount(amount, currency.Precision)
				if plan.PurchaseEnabled && amount > currency.MaxBalance && !hasError(issues, field+".price") {
					validationIssue(&issues, field+".price", "membership.price_exceeds_balance", "购买价格不能高于该货币的单账号余额上限。")
				}
			} else if plan.PurchaseEnabled && !hasError(issues, field+".price") {
				validationIssue(&issues, field+".price", "membership.price_precision_mismatch", fmt.Sprintf("价格最多支持该货币的 %d 位小数。", currency.Precision))
			}
			if plan.PurchaseEnabled && !currency.Enabled {
				previewWarning(&issues, field+".currency", "membership.currency_disabled", fmt.Sprintf("支付货币「%s」当前已停用，用户暂时无法购买此方案。", currency.Name))
			}
		} else if plan.PurchaseEnabled {
			validationIssue(&issues, field+".currency", "membership.currency_missing", "购买币种不存在，请重新选择。")
		}
		if plan.PurchaseEnabled && !plan.Enabled {
			previewWarning(&issues, field+".purchase_enabled", "membership.purchase_plan_disabled", "方案允许站内币购买但已停用；用户暂时无法选择。")
		}
		switch {
		case hasError(issues, field):
			preview.Status = "error"
		case hasWarning(issues, field):
			preview.Status = "warning"
		default:
			preview.Status = "ok"
		}
		plans = append(plans, preview)
	}

	levels := make([]PreviewLevel, 0, len(s.Levels.Items))
	for i, level := range s.Levels.Items {
		field := fmt.Sprintf("levels.items[%d]", i)
		gap := level.Threshold
		if i > 0 {
			gap -= s.Levels.Items[i-1].Threshold
		}
		status := "ok"
		if hasError(issues, field) {
			status = "error"
		} else if hasWarning(issues, field) {
			status = "warning"
		}
		levels = append(levels, PreviewLevel{Index: i, Key: level.Key, Name: level.Name, Threshold: strconv.FormatInt(level.Threshold, 10), Gap: strconv.FormatInt(gap, 10), Status: status})
	}

	enabledRewards := s.CheckIn.Enabled || s.Registration.Enabled || s.Publication.Enabled
	if !s.Experience.Enabled && enabledRewards {
		previewWarning(&issues, "experience.enabled", "experience.rewards_paused", "自动经验总开关关闭；签到、注册和发文奖励都不会发放。")
	}
	if s.Experience.Enabled && !enabledRewards {
		previewWarning(&issues, "experience.enabled", "experience.no_rewards_enabled", "自动经验总开关已开启，但三个奖励来源都关闭，目前不会发放经验。")
	}
	enabledPlans, purchasePlans := 0, 0
	maxPercent := 100
	for _, plan := range s.Membership.Plans {
		if plan.Enabled {
			enabledPlans++
		}
		if plan.PurchaseEnabled {
			purchasePlans++
		}
		if s.Membership.Enabled {
			maxPercent = max(maxPercent, plan.ExperiencePercent)
		}
	}
	if s.Membership.Enabled && enabledPlans == 0 {
		previewWarning(&issues, "membership.enabled", "membership.no_enabled_plans", "会员总开关已开启，但没有启用中的方案；管理员也无法开通或续期。")
	}
	if s.Membership.Enabled && purchasePlans == 0 {
		previewWarning(&issues, "membership.enabled", "membership.no_purchase_plans", "当前没有开放站内币购买的方案；会员仅能由管理员开通。")
	}
	if !s.Membership.Enabled && maxPercent > 100 {
		previewWarning(&issues, "membership.enabled", "membership.benefits_paused", "会员模块关闭，方案倍率不会作用于自动经验奖励。")
	}
	if ctx.MaximumExperience > 0 && s.Experience.Maximum > ctx.MaximumExperience && s.Experience.Maximum-ctx.MaximumExperience < s.Experience.DailyLimit {
		previewWarning(&issues, "experience.maximum", "experience.low_headroom", "现有最高经验距离账号上限不足一个每日奖励额度；奖励可能提前被上限拒绝。")
	}

	maxBase, maxAwarded := int64(0), int64(0)
	for _, amount := range []int64{s.CheckIn.Amount, s.Registration.Amount, s.Publication.Amount} {
		maxBase = max(maxBase, amount)
	}
	if maxBase > math.MaxInt64/int64(maxPercent) {
		maxAwarded = math.MaxInt64
	} else {
		maxAwarded = maxBase * int64(maxPercent) / 100
	}
	highest := int64(0)
	if len(s.Levels.Items) > 0 {
		highest = s.Levels.Items[len(s.Levels.Items)-1].Threshold
	}
	metrics := []SettingsMetric{
		{Key: "experience.maximum", Label: "账号经验上限", Value: strconv.FormatInt(s.Experience.Maximum, 10)},
		{Key: "experience.daily_limit", Label: "近 24 小时奖励上限", Value: strconv.FormatInt(s.Experience.DailyLimit, 10)},
		{Key: "rewards.max_base", Label: "单次最高基础经验", Value: strconv.FormatInt(maxBase, 10)},
		{Key: "rewards.max_awarded", Label: "单次最高到账（含倍率）", Value: strconv.FormatInt(maxAwarded, 10)},
		{Key: "levels.highest", Label: "最高等级门槛", Value: strconv.FormatInt(highest, 10)},
		{Key: "levels.count", Label: "等级数量", Value: strconv.Itoa(len(s.Levels.Items))},
		{Key: "membership.max_percent", Label: "最高经验倍率", Value: strconv.Itoa(maxPercent) + "%"},
		{Key: "membership.plans", Label: "启用 / 可购买方案", Value: strconv.Itoa(enabledPlans) + " / " + strconv.Itoa(purchasePlans)},
	}
	valid := true
	for _, issue := range issues {
		if issue.Severity == "error" {
			valid = false
			break
		}
	}
	return SettingsPreview{Valid: valid, Issues: issues, Metrics: metrics, Plans: plans, Levels: levels}
}
