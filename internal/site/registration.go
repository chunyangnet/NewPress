package site

import (
	"net/url"
	"newpress.local/newpress/internal/auth"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// RegistrationPolicy is public configuration. Provider credentials remain in
// the separate accessconfig store; admission codes are SHA-256 digests only.
type RegistrationPolicy struct {
	Referral             *auth.ReferralPolicy `json:"referral,omitempty"`
	Fields               RegistrationFields   `json:"fields"`
	EmailVerification    bool                 `json:"email_verification"`
	PhoneVerification    bool                 `json:"phone_verification"`
	PhoneRegion          *PhoneRegionPolicy   `json:"phone_region,omitempty"`
	CodeLoginEnabled     bool                 `json:"code_login_enabled"`
	PasswordConfirmation bool                 `json:"password_confirmation"`
	RegistrationCodeURL  string               `json:"registration_code_url,omitempty"`
	OAuthBindings        []string             `json:"oauth_bindings,omitempty"`
}

// PhoneRegion applies to enrollment and newly bound phone numbers. Existing
// verified numbers retain code-login access after a region policy change.
type PhoneRegionPolicy struct {
	Selectable  bool     `json:"selectable"`
	DefaultCode string   `json:"default_code"`
	Codes       []string `json:"codes"`
}

func DefaultPhoneRegion() PhoneRegionPolicy {
	return PhoneRegionPolicy{Selectable: true, DefaultCode: "+86", Codes: []string{"+86", "+852", "+853", "+886", "+1", "+44", "+81", "+82", "+65", "+60", "+61", "+49", "+33"}}
}

func (p RegistrationPolicy) PhoneRegions() PhoneRegionPolicy {
	if p.PhoneRegion == nil {
		return DefaultPhoneRegion()
	}
	r := *p.PhoneRegion
	r.Codes = append([]string{}, r.Codes...)
	return r
}

func (p RegistrationPolicy) AllowsPhone(phone string) bool {
	r := p.PhoneRegions()
	codes := r.Codes
	if !r.Selectable {
		codes = []string{r.DefaultCode}
	}
	for _, code := range codes {
		if strings.HasPrefix(phone, code) && len(phone) > len(code) {
			return true
		}
	}
	return false
}

var phoneCountryCode = regexp.MustCompile(`^\+[1-9][0-9]{0,2}$`)

type RegistrationFields struct {
	DisplayName      string `json:"display_name"`
	Email            string `json:"email"`
	Phone            string `json:"phone"`
	Username         string `json:"username"`
	Password         string `json:"password"`
	InvitationCode   string `json:"invitation_code"`
	RegistrationCode string `json:"registration_code"`
}

var LoginProviderNames = map[string]string{"qq": "QQ", "github": "GitHub", "wechat": "微信", "microsoft": "微软", "google": "Google", "linuxdo": "Linux DO"}
var LoginProviderOrder = []string{"qq", "github", "wechat", "microsoft", "google", "linuxdo"}

func DefaultRegistrationPolicy() RegistrationPolicy {
	region := DefaultPhoneRegion()
	return RegistrationPolicy{Fields: RegistrationFields{"required", "required", "hidden", "required", "required", "hidden", "hidden"}, PhoneRegion: &region, EmailVerification: true, CodeLoginEnabled: true, PasswordConfirmation: true}
}

func (p RegistrationPolicy) AllowsCodeLogin() bool {
	return p.CodeLoginEnabled
}

func (s Settings) RegistrationPolicy() RegistrationPolicy {
	if s.Accounts == nil || s.Accounts.Policy == nil {
		return DefaultRegistrationPolicy()
	}
	p := *s.Accounts.Policy
	if p.Referral != nil {
		copy := *p.Referral
		copy.Roles = append([]string(nil), copy.Roles...)
		p.Referral = &copy
	}
	p.OAuthBindings = append([]string(nil), p.OAuthBindings...)
	if p.PhoneRegion != nil {
		region := p.PhoneRegions()
		p.PhoneRegion = &region
	}
	return p
}

func (f RegistrationFields) Modes() map[string]string {
	return map[string]string{"display_name": f.DisplayName, "email": f.Email, "phone": f.Phone, "username": f.Username, "password": f.Password, "invitation_code": f.InvitationCode, "registration_code": f.RegistrationCode}
}

func validateRegistration(p *RegistrationPolicy, fields map[string]string) {
	if p.Referral != nil {
		if err := p.Referral.Validate(); err != nil {
			for key, message := range auth.UserFieldErrors(err) {
				fields[key] = message
			}
		}
	}
	if p.PhoneRegion != nil {
		r := p.PhoneRegion
		r.DefaultCode = strings.TrimSpace(r.DefaultCode)
		if !phoneCountryCode.MatchString(r.DefaultCode) {
			fields["account_phone_default_code"] = "请填写国际区号，例如 +86"
		}
		seen := map[string]bool{}
		for i, code := range r.Codes {
			code = strings.TrimSpace(code)
			r.Codes[i] = code
			if !phoneCountryCode.MatchString(code) || seen[code] {
				fields["account_phone_codes"] = "区号须为 + 和 1 至 3 位数字，且不能重复"
			}
			seen[code] = true
		}
		if len(r.Codes) > 250 || r.Selectable && (len(r.Codes) == 0 || !seen[r.DefaultCode]) {
			fields["account_phone_codes"] = "请配置可选区号，并包含默认区号（最多 250 项）"
		}
	}
	for key, mode := range p.Fields.Modes() {
		if !oneOf(mode, "hidden", "optional", "required") {
			fields["account_field_"+key] = "请选择隐藏、选填或必填"
		}
	}
	if p.Fields.Email == "hidden" {
		p.EmailVerification = false
	}
	if p.Fields.Phone == "hidden" {
		p.PhoneVerification = false
	}
	if p.Fields.Password == "hidden" {
		p.PasswordConfirmation = false
	}
	// A local account must retain a usable way to sign in after this session.
	if p.Fields.Password != "required" && (!p.AllowsCodeLogin() || !(p.Fields.Email == "required" && p.EmailVerification) && !(p.Fields.Phone == "required" && p.PhoneVerification)) {
		fields["account_field_password"] = "密码非必填时，须开启验证码登录，并将邮箱或手机号设为必填且开启验证"
	}
	if p.Fields.InvitationCode != "hidden" && !p.Referrals().Enabled {
		fields["account_field_invitation_code"] = "请先开启用户邀请分享，或将邀请码设为隐藏"
	}
	p.RegistrationCodeURL = strings.TrimSpace(p.RegistrationCodeURL)
	if p.RegistrationCodeURL != "" {
		u, err := url.Parse(p.RegistrationCodeURL)
		if err != nil || u.User != nil || len(p.RegistrationCodeURL) > 2048 || !((u.Scheme == "https" && u.Host != "") || (u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(p.RegistrationCodeURL, "//") && !strings.Contains(p.RegistrationCodeURL, "\\"))) {
			fields["account_registration_code_url"] = "请输入站内路径或 HTTPS 地址"
		}
	}
	seen := map[string]bool{}
	for _, key := range p.OAuthBindings {
		if !oneOf(key, "display_name", "email", "phone", "username", "password") || seen[key] {
			fields["account_oauth_bind_"+key] = "绑定字段不正确或重复"
		}
		if p.Fields.Modes()[key] == "hidden" {
			fields["account_oauth_bind_"+key] = "要求绑定的字段不能隐藏"
		}
		seen[key] = true
	}
}

func parseRegistration(values url.Values, boolean func(string) bool, fields map[string]string) *RegistrationPolicy {
	version := values.Get("account_policy_version")
	if version != "2" {
		fields["account_policy_version"] = "注册配置版本不正确，请刷新后重试"
		return nil
	}
	p := DefaultRegistrationPolicy()
	f := func(key string) string { return values.Get("account_field_" + key) }
	p.Fields = RegistrationFields{f("display_name"), f("email"), f("phone"), f("username"), f("password"), f("invitation_code"), f("registration_code")}
	p.EmailVerification = boolean("account_email_verification")
	p.PhoneVerification = boolean("account_phone_verification")
	p.CodeLoginEnabled = boolean("account_code_login_enabled")
	p.PhoneRegion = &PhoneRegionPolicy{Selectable: boolean("account_phone_region_selectable"), DefaultCode: values.Get("account_phone_default_code"), Codes: strings.FieldsFunc(values.Get("account_phone_codes"), func(r rune) bool { return r == ',' || r == '，' || r == ';' || r == '\n' || r == '\r' || r == ' ' })}
	p.PasswordConfirmation = boolean("account_password_confirmation")
	p.RegistrationCodeURL = values.Get("account_registration_code_url")
	if values.Has("account_referral_max_codes") {
		r := auth.DefaultReferralPolicy()
		r.Enabled = boolean("account_referral_enabled")
		r.VerifiedEmail = boolean("account_referral_verified_email")
		r.VerifiedPhone = boolean("account_referral_verified_phone")
		r.RewardEnabled = boolean("account_referral_reward_enabled")
		r.RewardVerifiedEmail = boolean("account_referral_reward_verified_email")
		r.RewardCurrency = strings.TrimSpace(values.Get("account_referral_reward_currency"))
		r.Roles = strings.FieldsFunc(values.Get("account_referral_roles"), func(c rune) bool { return c == ',' || c == ' ' || c == '，' })
		number := func(key string) int64 {
			n, err := strconv.ParseInt(values.Get("account_referral_"+key), 10, 64)
			if err != nil {
				fields["account_referral_"+key] = "请填写有效整数"
			}
			return n
		}
		r.MinAgeDays = int(number("min_age_days"))
		r.MaxCodes = int(number("max_codes"))
		r.DailyCodes = int(number("daily_codes"))
		r.CodeUses = number("code_uses")
		r.ExpiresDays = int(number("expires_days"))
		r.InviterReward = number("inviter_reward")
		r.InviteeReward = number("invitee_reward")
		if len(r.Roles) == 0 {
			r.Roles = nil
		}
		if !reflect.DeepEqual(r, auth.DefaultReferralPolicy()) {
			p.Referral = &r
		}
	}
	for _, key := range []string{"display_name", "email", "phone", "username", "password"} {
		if boolean("account_oauth_bind_" + key) {
			p.OAuthBindings = append(p.OAuthBindings, key)
		}
	}
	return &p
}

func registrationFields(settings Settings) []SettingField {
	p := settings.RegistrationPolicy()
	region := p.PhoneRegions()
	fields := []SettingField{optionField("account_registration", "accounts", "注册方式", "可使用邮件邀请或邀请码；关闭注册时已绑定的第三方账号仍可登录。", "select", settings.AccountRegistration(), []SettingOption{{"closed", "关闭注册", ""}, {"invite", "邀请注册", ""}, {"open", "开放注册", ""}})}
	labels := map[string]string{"display_name": "公开昵称", "email": "邮箱", "phone": "手机号", "username": "用户名", "password": "密码", "invitation_code": "邀请码", "registration_code": "注册码"}
	for _, key := range []string{"display_name", "username", "email", "phone", "password", "invitation_code", "registration_code"} {
		fields = append(fields, optionField("account_field_"+key, "accounts", labels[key], "隐藏字段不会提交；选填可留空，必填由服务器校验。", "select", p.Fields.Modes()[key], []SettingOption{{"hidden", "隐藏", ""}, {"optional", "选填", ""}, {"required", "必填", ""}}))
	}
	fields = append(fields,
		boolField("account_code_login_enabled", "accounts", "开启验证码登录", "允许通过已验证的邮箱或手机号登录；关闭后隐藏入口并禁止验证码登录，不影响注册和绑定验证。", p.AllowsCodeLogin(), false),
		boolField("account_email_verification", "accounts", "验证邮箱", "填写邮箱后需完成邮件验证码验证。", p.EmailVerification, false),
		boolField("account_phone_verification", "accounts", "验证手机号", "填写手机号后需完成短信验证码；请在「登录与短信」中配置并测试发送服务。", p.PhoneVerification, false),
		boolField("account_phone_region_selectable", "accounts", "显示手机区号下拉", "开启后可选择允许的区号；关闭后注册和绑定仅接受默认区号。已有账号仍可用原验证手机号登录。", region.Selectable, false),
		textField("account_phone_default_code", "accounts", "默认手机区号", "默认 +86；关闭区号下拉时，此处也是唯一允许的区号。", region.DefaultCode, true, 4, "+86", "text"),
		textField("account_phone_codes", "accounts", "允许的手机区号", "用逗号分隔，例如 +86,+852,+1。启用下拉时须包含默认区号。", strings.Join(region.Codes, ","), false, 1500, "+86,+852,+1", "text"),

		boolField("account_password_confirmation", "accounts", "再次输入密码", "填写密码时要求输入两次，并在服务器核对。", p.PasswordConfirmation, false),
		textField("account_registration_code_url", "accounts", "注册码获取链接", "展示在注册码字段旁，可填写站内路径或 HTTPS 地址。", p.RegistrationCodeURL, false, 2048, "/registration-code/", "url"))
	fields = append(fields, referralFields(p.Referrals())...)
	for _, key := range []string{"display_name", "email", "phone", "username", "password"} {
		enabled := false
		for _, binding := range p.OAuthBindings {
			enabled = enabled || binding == key
		}
		fields = append(fields, boolField("account_oauth_bind_"+key, "accounts", "快捷登录后绑定"+labels[key], "新账号及已有第三方账号缺少此信息时，完成绑定后才能登录。", enabled, false))
	}
	return fields
}

func init() {
	for _, field := range registrationFields(DefaultSettings("", "")) {
		formFields[field.Key] = struct{}{}
	}
	formFields["account_policy_version"] = struct{}{}
}
