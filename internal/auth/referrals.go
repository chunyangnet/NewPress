package auth

import (
	"context"
	"fmt"
	"time"
)

// ReferralPolicy is supplied by the server, never by a member's request body.
type ReferralPolicy struct {
	Enabled             bool     `json:"enabled"`
	MinAgeDays          int      `json:"min_age_days"`
	VerifiedEmail       bool     `json:"verified_email"`
	VerifiedPhone       bool     `json:"verified_phone"`
	Roles               []string `json:"roles,omitempty"`
	MaxCodes            int      `json:"max_codes"`
	DailyCodes          int      `json:"daily_codes"`
	CodeUses            int64    `json:"code_uses"`
	ExpiresDays         int      `json:"expires_days"`
	RewardEnabled       bool     `json:"reward_enabled"`
	RewardCurrency      string   `json:"reward_currency"`
	InviterReward       int64    `json:"inviter_reward"`
	InviteeReward       int64    `json:"invitee_reward"`
	RewardVerifiedEmail bool     `json:"reward_verified_email"`
}

func DefaultReferralPolicy() ReferralPolicy {
	return ReferralPolicy{MaxCodes: 5, DailyCodes: 1, CodeUses: 10, ExpiresDays: 30, RewardCurrency: "main", RewardVerifiedEmail: true}
}

func (p ReferralPolicy) Validate() error {
	if p.MinAgeDays < 0 || p.MinAgeDays > 3650 || p.MaxCodes < 1 || p.MaxCodes > 100 || p.DailyCodes < 1 || p.DailyCodes > 100 || p.CodeUses < 0 || p.CodeUses > 1000000 || p.ExpiresDays < 0 || p.ExpiresDays > 3650 || p.InviterReward < 0 || p.InviterReward > 1000000000 || p.InviteeReward < 0 || p.InviteeReward > 1000000000 {
		return codeField("account_referral_max_codes", "请检查邀请额度：最多保留 1–100 个，每日生成 1–100 个，金额不能为负数")
	}
	if len(p.Roles) > 30 || len(p.RewardCurrency) > 64 || p.RewardEnabled && p.RewardCurrency == "" {
		return codeField("account_referral_reward_currency", "请选择奖励币种")
	}
	seen := map[string]bool{}
	for _, role := range p.Roles {
		if !validRoleSlug(role) || seen[role] {
			return codeField("account_referral_roles", "角色标识格式不正确或重复")
		}
		seen[role] = true
	}
	if p.RewardEnabled && p.InviterReward == 0 && p.InviteeReward == 0 {
		return codeField("account_referral_inviter_reward", "开启奖励时，请至少设置一方的奖励金额")
	}
	return nil
}

type ReferralData struct {
	Codes                []RegistrationCode `json:"codes,omitempty"`
	InviterID            int64              `json:"inviter_id,omitempty"`
	CodeID               string             `json:"code_id,omitempty"`
	Currency             string             `json:"currency,omitempty"`
	InviterReward        int64              `json:"inviter_reward,omitempty"`
	InviteeReward        int64              `json:"invitee_reward,omitempty"`
	RequireVerifiedEmail bool               `json:"require_verified_email,omitempty"`
}

func CloneReferral(d ReferralData) ReferralData { d.Codes = CloneRegistrationCodes(d.Codes); return d }

func GenerateReferral(u User, p ReferralPolicy, now time.Time) (ReferralData, error) {
	d := CloneReferral(u.Referral)
	if err := p.Validate(); err != nil {
		return d, err
	}
	if !p.Enabled || u.Status != UserStatusActive {
		return d, codeField("invitation_code", "当前暂不能生成邀请码")
	}
	if now.Before(u.CreatedAt.Add(time.Duration(p.MinAgeDays) * 24 * time.Hour)) {
		return d, codeField("invitation_code", fmt.Sprintf("注册满 %d 天后可生成邀请码", p.MinAgeDays))
	}
	if p.VerifiedEmail && u.EmailVerifiedAt == nil || p.VerifiedPhone && u.PhoneVerifiedAt == nil {
		return d, codeField("invitation_code", "请先完成要求的邮箱或手机验证")
	}
	if len(p.Roles) > 0 {
		allowed := false
		for _, r := range u.Roles {
			for _, slug := range p.Roles {
				allowed = allowed || r.Slug == slug
			}
		}
		if !allowed {
			return d, codeField("invitation_code", "当前用户角色暂不支持生成邀请码")
		}
	}
	today := 0
	for _, c := range d.Codes {
		if c.CreatedAt.UTC().Format("2006-01-02") == now.UTC().Format("2006-01-02") {
			today++
		}
	}
	if len(d.Codes) >= p.MaxCodes || today >= p.DailyCodes {
		return d, codeField("invitation_code", "已达到邀请码总量或今日生成上限")
	}
	raw, err := RandomCredential()
	if err != nil {
		return d, err
	}
	c := RegistrationCode{ID: raw, Code: "I-" + raw, RegistrationCodeRules: RegistrationCodeRules{Enabled: true, MaxUses: p.CodeUses}, Version: 1, CreatedAt: now, UpdatedAt: now}
	if p.ExpiresDays > 0 {
		expires := now.Add(time.Duration(p.ExpiresDays) * 24 * time.Hour)
		c.ExpiresAt = &expires
	}
	d.Codes = append(d.Codes, c)
	return d, nil
}

func ReferralAttribution(owner User, hash string, p ReferralPolicy, now time.Time) (ReferralData, ReferralData, error) {
	d := CloneReferral(owner.Referral)
	if err := p.Validate(); err != nil {
		return d, ReferralData{}, err
	}
	if !p.Enabled || owner.Status != UserStatusActive {
		return d, ReferralData{}, codeField("invitation_code", "邀请码已失效或邀请人已停用")
	}
	for i, c := range d.Codes {
		if RegistrationCodeHash(c.Code) != hash {
			continue
		}
		if RegistrationCodeStatus(c, now) != "active" {
			break
		}
		d.Codes[i].Used++
		d.Codes[i].Version++
		d.Codes[i].LastUsedAt = &now
		d.Codes[i].UpdatedAt = now
		fact := ReferralData{InviterID: owner.ID, CodeID: c.ID}
		if p.RewardEnabled {
			fact.Currency, fact.InviterReward, fact.InviteeReward, fact.RequireVerifiedEmail = p.RewardCurrency, p.InviterReward, p.InviteeReward, p.RewardVerifiedEmail
		}
		return d, fact, nil
	}
	return d, ReferralData{}, codeField("invitation_code", "邀请码无效、已过期或使用次数已用完")
}

type ReferralStore interface {
	GenerateReferral(context.Context, int64, ReferralPolicy, time.Time) (ReferralData, error)
	SetReferralCode(context.Context, int64, string, bool) error
	ReferralUsers(context.Context, int64) ([]User, error)
}

func (m *Manager) GenerateReferral(ctx context.Context, id int64, p ReferralPolicy) (ReferralData, error) {
	s, ok := m.users.(ReferralStore)
	if !ok {
		return ReferralData{}, ErrUserStoreNotAvailable
	}
	return s.GenerateReferral(ctx, id, p, m.now())
}
func (m *Manager) SetReferralCode(ctx context.Context, id int64, code string, enabled bool) error {
	s, ok := m.users.(ReferralStore)
	if !ok {
		return ErrUserStoreNotAvailable
	}
	return s.SetReferralCode(ctx, id, code, enabled)
}
func (m *Manager) ReferralUsers(ctx context.Context, id int64) ([]User, error) {
	s, ok := m.users.(ReferralStore)
	if !ok {
		return nil, ErrUserStoreNotAvailable
	}
	return s.ReferralUsers(ctx, id)
}
