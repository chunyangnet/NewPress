package site

import (
	"newpress.local/newpress/internal/auth"
	"strconv"
	"strings"
)

func (p RegistrationPolicy) Referrals() auth.ReferralPolicy {
	if p.Referral == nil {
		return auth.DefaultReferralPolicy()
	}
	r := *p.Referral
	r.Roles = append([]string(nil), r.Roles...)
	return r
}
func referralFields(r auth.ReferralPolicy) []SettingField {
	f := []SettingField{
		boolField("account_referral_enabled", "accounts", "用户邀请分享", "用户在个人中心生成专属邀请码和分享链接。选填邀请码用于邀请归因，必填或邀请注册用于限制准入。", r.Enabled, false),
		boolField("account_referral_verified_email", "accounts", "生成前验证邮箱", "邀请人需要有已验证邮箱。", r.VerifiedEmail, false),
		boolField("account_referral_verified_phone", "accounts", "生成前验证手机", "邀请人需要有已验证手机号。", r.VerifiedPhone, false),
		textField("account_referral_roles", "accounts", "允许生成的角色", "填写角色标识，用逗号分隔；留空允许所有正常账号。例：subscriber,author。", strings.Join(r.Roles, ","), false, 300, "所有正常账号", "text"),
		boolField("account_referral_reward_enabled", "accounts", "邀请奖励", "按新用户注册时的规则记录奖励，双方各发放一次；币种暂停时保留待领取奖励。", r.RewardEnabled, false),
		textField("account_referral_reward_currency", "accounts", "奖励币种标识", "填写虚拟币管理中的币种标识，默认 main。奖励金额按币种最小单位填写。", r.RewardCurrency, false, 64, "main", "text"),
		boolField("account_referral_reward_verified_email", "accounts", "奖励前验证新用户邮箱", "新用户完成邮箱验证后才能领取双方奖励。", r.RewardVerifiedEmail, false),
	}
	for _, item := range []struct {
		key, title, description string
		value                   int64
	}{
		{"min_age_days", "生成所需注册天数", "0 表示注册后即可生成。", int64(r.MinAgeDays)},
		{"max_codes", "每人累计生成上限", "包含停用、过期的邀请码；提高上限后用户可继续生成。", int64(r.MaxCodes)},
		{"daily_codes", "每天生成上限", "按 UTC 自然日统计，1–100 个。", int64(r.DailyCodes)},
		{"code_uses", "每个邀请码可用次数", "0 表示不限次数，仅影响之后生成的码。", r.CodeUses},
		{"expires_days", "邀请码有效天数", "0 表示永久有效，仅影响之后生成的码。", int64(r.ExpiresDays)},
		{"inviter_reward", "邀请人奖励", "每成功邀请一个新用户发放的最小单位数量；0 表示不奖励邀请人。", r.InviterReward},
		{"invitee_reward", "新用户奖励", "被邀请的新用户获得的最小单位数量；0 表示不奖励新用户。", r.InviteeReward},
	} {
		f = append(f, textField("account_referral_"+item.key, "accounts", item.title, item.description, strconv.FormatInt(item.value, 10), true, 12, "0", "number"))
	}
	return f
}
