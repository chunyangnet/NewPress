package httpserver

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/virtualcurrency"
)

func (s *Server) progressionFeatureStatus(ctx context.Context) []featureStatusItem {
	if s.progression == nil || s.progression.Store == nil {
		return []featureStatusItem{featureFailure("progression", "成长与会员", "成长服务尚未装配，请检查系统状态。")}
	}
	cfg, err := s.progression.Store.Settings(ctx)
	if err != nil {
		return []featureStatusItem{featureFailure("progression", "成长与会员", "成长规则暂时不可读，请重新检测。")}
	}
	config := func(label, section string) *featureStatusAction {
		return featureFocus(label, "/admin/progression/#progression-"+section, "#progression-"+section+" input[type=checkbox]", auth.CapabilitySettingsManage)
	}
	checkin := featureStatusItem{ID: "progression-checkin", Title: "成长签到", Summary: "成长签到已开启，领取仍受邮箱验证、签到周期与奖励额度限制。", Checks: []featureStatusCheck{
		featureCheck("experience", "自动经验", cfg.Experience.Enabled, "paused", "自动经验已关闭，签到经验不会发放。", config("配置经验规则", "experience")),
		featureCheck("checkin", "签到奖励", cfg.CheckIn.Enabled, "paused", "签到经验已关闭，其他经验规则独立管理。", config("配置签到经验", "check-in")),
	}}
	if cfg.Experience.RequireVerifiedEmail {
		checkin.Checks = append(checkin.Checks, featureInfo("eligibility", "领取条件", "用户需验证邮箱；每位用户的签到周期与额度单独计算。"))
	}
	levels := featureStatusItem{ID: "progression-levels", Title: "等级展示", Summary: "等级功能已开启，公开徽章跟随已保存的展示偏好。", Checks: []featureStatusCheck{
		featureCheck("levels", "等级功能", cfg.Levels.Enabled, "paused", "等级功能已关闭。", config("配置等级门槛", "levels")),
		featureInfo("visibility", "公开徽章", map[bool]string{true: "公开展示等级徽章。", false: "公开徽章已隐藏，等级功能保持独立。"}[cfg.Levels.PublicBadges]),
	}}
	member := featureStatusItem{ID: "membership", Title: "会员功能", Summary: "会员功能已开启，开通条件由各会员方案决定。", Checks: []featureStatusCheck{
		featureCheck("membership", "会员功能", cfg.Membership.Enabled, "paused", "会员功能已关闭，已有记录保留。", config("配置会员方案", "membership")),
		featureInfo("visibility", "公开徽章", map[bool]string{true: "仅有效会员展示公开徽章。", false: "会员公开徽章已隐藏。"}[cfg.Membership.PublicBadges]),
	}}
	return []featureStatusItem{checkin, levels, member, s.homeCheckInFeatureStatus(ctx)}
}

func (s *Server) currencyFeatureStatus(ctx context.Context, key string) []featureStatusItem {
	if key == "" {
		key = virtualcurrency.DefaultCurrencyKey
	}
	if !virtualcurrency.ValidCurrencyKey(key) {
		return []featureStatusItem{featureFailure("currency", "钱包", "货币标识无效。")}
	}
	store := s.currency
	if s.currencyCatalog != nil {
		var err error
		store, err = s.currencyCatalog.Store(ctx, key)
		if err != nil {
			return []featureStatusItem{featureFailure("currency", "钱包", "该货币不可用，请检查货币目录。")}
		}
	} else if key != virtualcurrency.DefaultCurrencyKey {
		store = nil
	}
	if store == nil {
		return []featureStatusItem{featureFailure("currency", "钱包", "钱包服务尚未装配。")}
	}
	cfg, err := store.Settings(ctx)
	if err != nil {
		return []featureStatusItem{featureFailure("currency", "钱包", "钱包规则暂时不可读，请重新检测。")}
	}
	href := "/admin/currency/?currency=" + key
	config := func(label, name string) *featureStatusAction {
		return featureFocus(label, href, "[data-currency-admin] [name="+name+"]", auth.CapabilitySettingsManage)
	}
	base := featureCheck("wallet", "钱包启用", cfg.Enabled, "paused", "此钱包已关闭，余额与记录保留。", config("配置钱包", "enabled"))
	checkin := featureStatusItem{ID: "wallet-checkin", Title: cfg.Name + " · 签到", Scope: "当前货币", Summary: "签到已开启，领取仍受验证、签到周期和奖励额度限制。", Checks: []featureStatusCheck{base,
		featureCheck("checkin", "签到奖励", cfg.CheckInEnabled, "paused", "此货币的签到奖励已关闭。", config("配置签到奖励", "check_in_enabled")),
		featureInfo("scope", "首页来源", "首页优先采用成长签到，其次使用主钱包签到；其他货币在对应钱包内领取。"),
	}}
	transfer := featureStatusItem{ID: "wallet-transfers", Title: cfg.Name + " · 转账", Scope: "当前货币", Summary: "允许转账；打赏还需在互动与身份中选择此钱包。", Checks: []featureStatusCheck{base,
		featureCheck("transfers", "转账规则", cfg.AllowTransfers, "paused", "此钱包未允许转账，因此也不能用于文章打赏。", config("配置转账规则", "allow_transfers")),
		featureInfo("limits", "单笔范围", currencyAmount(cfg.MinTransfer, cfg.Precision)+" 至 "+currencyAmount(cfg.MaxTransfer, cfg.Precision)+" "+cfg.Name+"；余额和每日限额按用户核验。"),
	}}
	return []featureStatusItem{checkin, transfer, s.homeCheckInFeatureStatus(ctx)}
}

func (s *Server) recognitionFeatureStatus(ctx context.Context) []featureStatusItem {
	if s.recognition == nil || s.recognition.Backend == nil {
		return []featureStatusItem{featureFailure("recognition", "互动与身份", "互动与身份服务尚未装配。")}
	}
	st, err := s.recognition.Backend.Read(ctx)
	if err != nil {
		return []featureStatusItem{featureFailure("recognition", "互动与身份", "互动规则暂时不可读，请重新检测。")}
	}
	cfg := st.Settings
	v := featureStatusItem{ID: "tips", Title: "文章打赏", Summary: "打赏已开启，可用钱包按余额与转账规则执行。", Checks: []featureStatusCheck{
		featureCheck("tips", "打赏开关", cfg.TipsEnabled, "paused", "文章打赏已关闭。开启后影响全站文章，不改变钱包转账规则。", featureFocus("配置文章打赏", "/admin/recognition/#recognition-tips", "[name=tips_enabled]", auth.CapabilitySettingsManage)),
	}}
	repair := featureFocus("调整打赏钱包", "/admin/recognition/#recognition-tips", "[data-recognition-currencies]", auth.CapabilitySettingsManage)
	usable, unreadable, invalidPresets := 0, false, 0
	wallets := []featureStatusCheck{}
	for _, choice := range cfg.Currencies {
		state, reason := "info", "此钱包已关闭或未允许转账。"
		action := featureLink("配置此钱包", "/admin/currency/?currency="+choice.Key, auth.CapabilitySettingsManage)
		var preset *featureStatusCheck
		if s.recognition.Currencies == nil {
			unreadable, reason = true, "钱包服务尚未装配，请检查系统状态。"
			action = featureLink("查看系统状态", "/admin/system/", auth.CapabilitySystemRead)
		} else {
			store, e := s.recognition.Currencies.Store(ctx, choice.Key)
			if e == nil {
				rules, e := store.Settings(ctx)
				if e != nil {
					unreadable = true
					reason = "暂时无法读取该钱包规则。"
				} else if rules.Enabled && rules.AllowTransfers {
					usable++
					state, reason = "ready", "钱包与转账已开启。单笔范围 "+currencyAmount(rules.MinTransfer, rules.Precision)+" 至 "+currencyAmount(rules.MaxTransfer, rules.Precision)+"。"
					for _, raw := range choice.Presets {
						amount, err := virtualcurrency.ParseAmount(raw, rules.Precision)
						if err != nil || amount < rules.MinTransfer || amount > rules.MaxTransfer {
							invalidPresets++
							preset = &featureStatusCheck{ID: "presets-" + choice.Key, Label: "快捷数值 · " + choice.Key, State: "info",
								Reason: "部分快捷数值不符合当前钱包的精度或单笔范围；合法的自定义金额仍可打赏，请调整后保存。",
								Action: featureFocus("调整快捷数值", "/admin/recognition/#recognition-tips", "[data-currency=\""+choice.Key+"\"] [data-currency-presets]", auth.CapabilitySettingsManage)}
							break
						}
					}
				}
			} else if errors.Is(e, virtualcurrency.ErrCurrencyNotFound) {
				reason, action = "所选钱包已不存在，请取消选择或改用其他钱包。", repair
			} else {
				unreadable, reason = true, "暂时无法读取该钱包，请重新检测。"
			}
		}
		wallets = append(wallets, featureStatusCheck{ID: "wallet-" + choice.Key, Label: "钱包 " + choice.Key, State: state, Reason: reason, Action: action})
		if preset != nil {
			wallets = append(wallets, *preset)
		}
	}
	state, reason := "needs_config", "尚无已开启且允许转账的打赏钱包，请选择或配置钱包。"
	walletAction := repair
	if unreadable && usable == 0 {
		state, reason = "error", "打赏钱包规则暂时不可读，请重新检测。"
		walletAction = featureLink("查看系统状态", "/admin/system/", auth.CapabilitySystemRead)
	}
	if s.recognition.Currencies == nil {
		state, reason = "error", "钱包服务尚未装配，请检查系统状态。"
		walletAction = featureLink("查看系统状态", "/admin/system/", auth.CapabilitySystemRead)
	}
	v.Checks = append(v.Checks, featureCheck("wallets", "可用打赏钱包", usable > 0, state, reason, walletAction))
	v.Checks = append(v.Checks, wallets...)
	if usable > 0 && (usable < len(cfg.Currencies) || invalidPresets > 0) {
		v.Partial = true
		v.Action = repair
		v.Summary = fmt.Sprintf("%d 个所选钱包可用于打赏，其余钱包暂不可用，原因见详情。", usable)
		if invalidPresets > 0 {
			v.Summary = fmt.Sprintf("%d 个所选钱包可用于打赏，部分快捷数值不符合当前钱包规则。", usable)
			if usable < len(cfg.Currencies) {
				v.Summary += "其余钱包暂不可用，原因见详情。"
			}
		}
	}
	identity := featureStatusItem{ID: "identity", Title: "公开身份展示", Summary: "公开展示取决于设计启用状态、有效授予与佩戴设置。"}
	enabled := 0
	for _, badge := range cfg.Badges {
		if badge.Enabled {
			enabled++
		}
	}
	identity.Checks = []featureStatusCheck{featureInfo("designs", "身份设计", fmt.Sprintf("已启用 %d 项设计；停用设计会隐藏其公开展示。", enabled)),
		featureInfo("verification", "创作者认证", map[bool]string{true: "已开放认证申请与授予；具体设计仍需启用。", false: "认证申请与授予已关闭，其他身份类型独立管理。"}[cfg.VerificationEnabled]),
		featureInfo("grants", "授予与佩戴", "头衔、勋章和认证仍需有效授予；等级和会员徽章在成长与会员中管理。")}
	return []featureStatusItem{v, identity}
}

// Homepage rendering and the administrator's explanation use the same resolver.
// A usable wallet remains a fallback when growth is paused or unreadable.
func (s *Server) resolveHomeCheckIn(ctx context.Context) (homeCheckInView, currencySettingsDTO, error) {
	var failures []error
	if s.progression != nil && s.progression.Store != nil {
		cfg, err := s.progression.Store.Settings(ctx)
		if err != nil {
			failures = append(failures, err)
		} else if cfg.Experience.Enabled && cfg.CheckIn.Enabled {
			return homeCheckInView{Enabled: true, Source: "progression", Reward: strconv.FormatInt(cfg.CheckIn.Amount, 10), Unit: cfg.Experience.Name,
				Mode: cfg.CheckIn.Rule().Mode, Timezone: cfg.CheckIn.Rule().Timezone, IntervalHours: cfg.CheckIn.IntervalHours, AccountURL: "/account/?view=progression"}, currencySettingsDTO{}, nil
		}
	}
	if s.currency != nil {
		cfg, err := s.currency.Settings(ctx)
		if err != nil {
			failures = append(failures, err)
		} else if cfg.Enabled && cfg.CheckInEnabled {
			coin := currencySettingsView(cfg)
			return homeCheckInView{Enabled: true, Source: "wallet", Reward: coin.CheckInReward, Unit: coin.Name, Mode: coin.CheckInMode,
				Timezone: coin.CheckInTimezone, IntervalHours: coin.CheckInIntervalHours, AccountURL: "/account/?view=wallet"}, coin, nil
		}
	}
	return homeCheckInView{}, currencySettingsDTO{}, errors.Join(failures...)
}

func (s *Server) homeCheckInFeatureStatus(ctx context.Context) featureStatusItem {
	v := featureStatusItem{ID: "home-checkin", Title: "首页签到来源", Scope: "首页", Summary: "签到来源按成长签到优先、主钱包其次的规则自动选择。"}
	chosen, _, err := s.resolveHomeCheckIn(ctx)
	if err != nil {
		v.Checks = []featureStatusCheck{{ID: "source", Label: "可用签到来源", State: "error", Reason: "签到规则暂时不可读，无法确认首页来源。"}}
		return v
	}
	v.Checks = append(v.Checks, featureCheck("source", "可用签到来源", chosen.Enabled, "paused", "成长签到和主钱包签到均未开启。配置其中一种即可提供首页签到。", featureLink("配置成长签到", "/admin/progression/#progression-check-in", auth.CapabilitySettingsManage)))
	if chosen.Enabled {
		v.Mode = map[string]string{"progression": "成长签到", "wallet": "主钱包签到"}[chosen.Source]
		v.Summary = "当前采用" + v.Mode + "，每次奖励 " + chosen.Reward + " " + chosen.Unit + "。"
	}
	visible := homeRequested(s.themes.SettingsSnapshot().Values, "checkin")
	label := "已保存的首页布局包含签到入口。"
	if !visible {
		label = "已保存的首页布局未展示签到入口；业务规则独立保留。"
	}
	entry := featureInfo("entry", "入口展示", label)
	if !visible {
		entry.Action = featureLink("编排首页入口", "/admin/appearance/layout/", auth.CapabilityAppearanceManage)
	}
	v.Checks = append(v.Checks, entry)
	return v
}
