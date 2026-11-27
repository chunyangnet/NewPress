package httpserver

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"time"

	"newpress.local/newpress/internal/accessconfig"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

func (s *Server) SetAccessSettings(store *accessconfig.Store) {
	if store != nil {
		s.accessSettings = store
	}
}

func (s *Server) accessConfig() accessconfig.Settings {
	return s.accessSettings.Runtime()
}

func (s *Server) accessSettingsView() map[string]any {
	settings := accessconfig.Redact(s.accessConfig())
	if settings.SMS == nil {
		sms := accessconfig.DefaultSMS()
		settings.SMS = &sms
	}
	applications := map[string]string{
		"qq":        "https://connect.qq.com/",
		"github":    "https://github.com/settings/applications/new",
		"wechat":    "https://open.weixin.qq.com/",
		"microsoft": "https://entra.microsoft.com/",
		"google":    "https://console.developers.google.com/auth/clients",
		"linuxdo":   "https://connect.linux.do/",
	}
	providers := []map[string]string{}
	for _, id := range site.LoginProviderOrder {
		providers = append(providers, map[string]string{"id": id, "name": site.LoginProviderNames[id], "callback": s.oauthRedirect(id), "application_url": applications[id]})
	}
	return map[string]any{"settings": settings, "providers": providers, "sms_providers": accessconfig.SMSCatalog(), "default_callback_base_url": s.config.PublicURL, "sms_ready": s.smsReady()}
}

func (s *Server) adminAccessSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	writeJSON(w, 200, s.accessSettingsView())
}

func (s *Server) adminAccessSettingsSave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input accessconfig.Settings
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 128<<10, "128 KiB"); !ok {
		return
	}
	before := s.accessConfig()
	if _, err := s.accessSettings.Save(input); err != nil {
		status := 422
		if errors.Is(err, accessconfig.ErrConflict) {
			status = 409
		}
		// Persistence errors can include local paths; only validation errors are shown.
		var pathErr *os.PathError
		message := err.Error()
		if errors.As(err, &pathErr) {
			status, message = 500, "登录与短信设置写入失败，原配置保持不变"
		}
		s.writeAPIError(w, r, status, "access.settings_invalid", message, nil)
		return
	}
	if loginConfigurationDigest(before) != loginConfigurationDigest(s.accessConfig()) {
		s.registration.mu.Lock()
		s.registration.states = map[string]oauthState{}
		s.registration.pending = map[string]oauthPending{}
		s.registration.mu.Unlock()
	}
	writeJSON(w, 200, s.accessSettingsView())
}

func loginConfigurationDigest(cfg accessconfig.Settings) string {
	ordered := []accessconfig.Provider{}
	for _, id := range site.LoginProviderOrder {
		for _, p := range cfg.Providers {
			if p.ID == id {
				ordered = append(ordered, p)
			}
		}
	}
	encoded, _ := json.Marshal(struct {
		Base      string
		Providers []accessconfig.Provider
	}{cfg.CallbackBaseURL, ordered})
	return secretDigest(string(encoded))
}

func (s *Server) adminAccessSMSTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		Phone    string `json:"phone"`
		Revision string `json:"revision"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 4096, "4 KiB"); !ok {
		return
	}
	cfg := s.accessConfig()
	if input.Revision == "" || input.Revision != cfg.Revision {
		s.writeAPIError(w, r, 409, "access.settings_changed", "请先保存或重新加载配置，再发送测试短信", nil)
		return
	}
	if !auth.ValidPhone(input.Phone) {
		s.writeAPIError(w, r, 422, "access.phone_invalid", "请填写带国际区号的完整手机号，例如 +8613800138000", nil)
		return
	}
	if !accessconfig.SMSReady(cfg.SMS) {
		s.writeAPIError(w, r, 422, "access.sms_unavailable", "请先启用并保存有效的短信配置", nil)
		return
	}
	if allowed, retry := s.accessRate.allow(fmt.Sprintf("sms-test:%d", principal.UserID), 3, time.Now()); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		s.writeAPIError(w, r, 429, "access.rate_limited", "测试次数过多，请稍后再试", nil)
		return
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err == nil {
		err = accessconfig.SendSMS(r.Context(), *cfg.SMS, input.Phone, fmt.Sprintf("%06d", n.Int64()), "test", s.registration.client)
	}
	if err != nil {
		s.writeAPIError(w, r, 502, "access.sms_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": "短信服务已接受测试请求，请检查手机及服务商送达记录。测试码不能用于登录或注册。"})
}
