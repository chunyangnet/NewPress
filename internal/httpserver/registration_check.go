package httpserver

import (
	"fmt"
	"net/http"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
	"time"
)

type registrationCheck struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func (s *Server) adminRegistrationCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, readonly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if readonly {
		s.writeAPIError(w, r, 403, "auth.required", "请先登录管理员账号", nil)
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	snap := s.siteSettings.Snapshot()
	p := snap.Settings.RegistrationPolicy()
	checks := []registrationCheck{}
	add := func(key, title string, ok bool, message string) {
		checks = append(checks, registrationCheck{key, title, ok, message})
	}
	mode := snap.Settings.AccountRegistration()
	add("account_registration", "注册方式", true, map[string]string{"closed": "当前不接受新用户注册，已有用户仍可登录。", "open": "已开放注册，新用户按下方规则填写资料。", "invite": "仅接受有效邀请，新用户仍须满足其他必填条件。"}[mode])
	add("account_email_verification", "邮箱验证", !p.EmailVerification || s.accessDeliveryReady(), map[bool]string{true: "邮件发送服务已配置，可用于邮箱验证；实际送达请通过邮件测试确认。", false: "邮件发送服务未配置；开启邮箱验证后，新用户将无法完成这一步。"}[s.accessDeliveryReady()])
	if !p.EmailVerification {
		checks[len(checks)-1].Message = "未要求邮箱验证。填写的邮箱不会自动标记为已验证。"
	}
	add("account_phone_verification", "手机验证", !p.PhoneVerification || s.smsReady(), map[bool]string{true: "短信服务配置已齐全，实际送达可在登录与短信中发送测试。", false: "短信服务尚不可用；如需验证手机，请先配置发送服务。"}[s.smsReady()])
	if !p.PhoneVerification {
		checks[len(checks)-1].Message = "未要求手机验证。"
	}
	add("account_code_login_enabled", "验证码登录", !p.CodeLoginEnabled || s.smsReady() || s.accessDeliveryReady(), "验证码登录已关闭。")
	if p.CodeLoginEnabled {
		checks[len(checks)-1].Message = map[bool]string{true: "已有可用发送通道，用户可使用已验证的联系方式登录。", false: "开关已开启，但邮件和短信均不可用，暂时无法发送登录验证码。"}[s.smsReady() || s.accessDeliveryReady()]
	}
	codes, err := s.auth.ListRegistrationCodes(r.Context())
	active := 0
	for _, c := range codes {
		if auth.RegistrationCodeStatus(c, time.Now()) == "active" {
			active++
		}
	}
	add("account_field_registration_code", "注册码", err == nil && (p.Fields.RegistrationCode != "required" || active > 0), fmt.Sprintf("当前有 %d 个可用注册码。", active))
	if p.Fields.RegistrationCode == "hidden" {
		checks[len(checks)-1].Message = "注册不使用注册码，码库中的记录仍会保留。"
	}
	if err != nil {
		checks[len(checks)-1].Message = "暂时无法读取注册码库，请重试。"
	}
	rp := p.Referrals()
	add("account_referral_enabled", "用户邀请", rp.Enabled || p.Fields.InvitationCode != "required", map[bool]string{true: "用户可按资格条件生成专属邀请码；填写有效码后记录邀请关系。", false: "用户邀请分享尚未开启。"}[rp.Enabled])
	add("account_field_invitation_code", "邀请码规则", rp.Enabled || p.Fields.InvitationCode != "required", "邀请码选填时用于邀请归因，必填时限制新用户注册。")
	if mode == "invite" && !rp.Enabled {
		checks[len(checks)-1].Message = "目前仅能通过邮件邀请注册；也可开启用户邀请分享。"
	}
	rewardOK := true
	rewardMessage := "邀请奖励未开启，只记录邀请关系。"
	if rp.RewardEnabled {
		rewardOK = false
		rewardMessage = "奖励已开启，但奖励币种不可用。"
		if s.currencyCatalog != nil {
			if ledger, e := s.currencyCatalog.Store(r.Context(), rp.RewardCurrency); e == nil {
				if cfg, e := ledger.Settings(r.Context()); e == nil && cfg.Enabled {
					rewardOK = rp.Enabled
					rewardMessage = "奖励币种可用，符合条件后在个人中心领取，重复领取不会重复发放。"
				}
			}
		}
		if !rp.Enabled {
			rewardMessage = "请先开启用户邀请分享，新注册才会记录邀请奖励。"
		}
	}
	add("account_referral_reward_enabled", "邀请奖励", rewardOK, rewardMessage)
	for _, id := range site.LoginProviderOrder {
		_, ready := s.loginProvider(id)
		enabled := false
		for _, provider := range s.accessConfig().Providers {
			if provider.ID == id {
				enabled = provider.Enabled
			}
		}
		ready = ready && s.oauthRedirect(id) != ""
		message := "尚未开启，不影响其他注册方式。"
		if enabled {
			message = map[bool]string{true: "配置齐全，可发起授权；实际登录还需平台端登记相同回调地址。", false: "已开启但应用凭据或回调地址不完整。"}[ready]
		}
		add("provider_"+id, site.LoginProviderNames[id]+" 登录", !enabled || ready, message)
	}
	writeJSON(w, 200, map[string]any{"items": checks, "revision": snap.Revision, "checked_at": time.Now().UTC()})
}
