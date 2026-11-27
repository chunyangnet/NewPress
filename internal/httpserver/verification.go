package httpserver

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/verification"
)

func (s *Server) SetVerification(store *verification.Store) {
	if store != nil {
		s.verification = verification.NewService(store)
	}
}

func (s *Server) verificationConfigFor(r *http.Request, scene, preview string) verification.PublicConfig {
	p, ok := s.currentAdmin(r)
	return s.verification.Config(scene, ok && p.UserID > 0, preview)
}
func (s *Server) checkVerification(r *http.Request, scene, token string) error {
	return s.verification.Consume(s.verificationConfigFor(r, scene, ""), s.commentGuard.sourceDigest(s.clientIPs.string(r)), token)
}
func (s *Server) requireVerification(w http.ResponseWriter, r *http.Request, scene, token string) bool {
	if err := s.checkVerification(r, scene, token); err != nil {
		s.verificationError(w, r, err)
		return false
	}
	return true
}
func (s *Server) verificationError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := 422, "invalid"
	if errors.Is(err, verification.ErrRequired) {
		code = "required"
	}
	if errors.Is(err, verification.ErrUnavailable) {
		status, code = 503, "unavailable"
	}
	s.writeAPIError(w, r, status, "verification."+code, err.Error(), nil)
}
func (s *Server) publicVerificationConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"scene": true, "preview": true})
	if !ok {
		return
	}
	if !verification.ValidScene(q.Get("scene")) && q.Get("scene") != "preview" {
		s.verificationError(w, r, verification.ErrInvalid)
		return
	}
	preview := q.Get("preview")
	if preview != "" || q.Get("scene") == "preview" {
		if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
			return
		}
		if q.Get("scene") != "preview" || !verificationProviderKnown(preview) {
			s.verificationError(w, r, verification.ErrInvalid)
			return
		}
	}
	writeJSON(w, 200, s.verificationConfigFor(r, q.Get("scene"), preview))
}
func verificationProviderKnown(id string) bool {
	for _, p := range verification.Catalog() {
		if p.ID == id {
			return true
		}
	}
	return false
}

type verificationRequest struct {
	Scene   string             `json:"scene"`
	Preview string             `json:"preview,omitempty"`
	Proof   verification.Proof `json:"proof"`
}

func (s *Server) readVerificationRequest(w http.ResponseWriter, r *http.Request) (verificationRequest, verification.PublicConfig, bool) {
	var input verificationRequest
	if !s.allowAccess(w, r, "verification", 40) {
		return input, verification.PublicConfig{}, false
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return input, verification.PublicConfig{}, false
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 16<<10, "16 KiB"); !ok {
		return input, verification.PublicConfig{}, false
	}
	if input.Preview != "" || input.Scene == "preview" {
		if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage); !ok {
			return input, verification.PublicConfig{}, false
		}
		if input.Scene != "preview" || !verificationProviderKnown(input.Preview) {
			s.verificationError(w, r, verification.ErrInvalid)
			return input, verification.PublicConfig{}, false
		}
	} else if !verification.ValidScene(input.Scene) {
		s.verificationError(w, r, verification.ErrInvalid)
		return input, verification.PublicConfig{}, false
	}
	return input, s.verificationConfigFor(r, input.Scene, input.Preview), true
}
func (s *Server) verificationChallenge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, c, ok := s.readVerificationRequest(w, r)
	if !ok {
		return
	}
	challenge, err := s.verification.Challenge(c, s.commentGuard.sourceDigest(s.clientIPs.string(r)))
	if err != nil {
		s.verificationError(w, r, err)
		return
	}
	writeJSON(w, 200, challenge)
}
func (s *Server) verificationVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	input, c, ok := s.readVerificationRequest(w, r)
	if !ok {
		return
	}
	u, _ := url.Parse(s.config.PublicURL)
	host := ""
	if u != nil {
		host = u.Hostname()
	}
	ip := s.clientIPs.string(r)
	token, err := s.verification.Verify(r.Context(), c, s.commentGuard.sourceDigest(ip), ip, host, input.Proof)
	if err != nil {
		s.verificationError(w, r, err)
		return
	}
	if input.Preview != "" {
		_ = s.verification.Consume(c, s.commentGuard.sourceDigest(ip), token)
		writeJSON(w, 200, map[string]any{"ok": true, "preview": true})
		return
	}
	writeJSON(w, 200, map[string]any{"token": token, "expires_in": 120})
}
func (s *Server) adminVerificationSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"settings": s.verification.Store.Snapshot(), "providers": verification.Catalog()})
}
func (s *Server) adminVerificationSave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input verification.Settings
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 64<<10, "64 KiB"); !ok {
		return
	}
	saved, err := s.verification.Store.Save(input)
	if err != nil {
		status := 422
		if errors.Is(err, verification.ErrConflict) {
			status = 409
		}
		s.writeAPIError(w, r, status, "verification.settings_invalid", err.Error(), nil)
		return
	}
	writeJSON(w, 200, saved)
}

// Only selected providers are allowed on public pages. The settings page also
// permits catalog origins so administrators can test a saved provider in place.
func (s *Server) verificationHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		settings := s.verification.Store.Snapshot()
		ids := map[string]bool{}
		if r.URL.Path == "/admin/settings/" {
			for _, p := range verification.Catalog() {
				ids[p.ID] = true
			}
		} else if settings.Enabled {
			for _, rule := range settings.Scenes {
				if rule.Enabled {
					id := rule.Provider
					if id == "" {
						id = settings.DefaultProvider
					}
					ids[id] = true
				}
			}
		}
		scripts, frames, connections, styles, images := []string{}, []string{}, []string{}, []string{}, []string{}
		for _, id := range []string{"turnstile", "geetest", "tencent", "hcaptcha", "recaptcha_v2", "recaptcha_v3"} {
			if !ids[id] {
				continue
			}
			switch id {
			case "turnstile":
				scripts = append(scripts, "https://challenges.cloudflare.com")
				frames = append(frames, "https://challenges.cloudflare.com")
				connections = append(connections, "https://challenges.cloudflare.com")
			case "geetest":
				scripts = append(scripts, "https://*.geetest.com", "https://*.geevisit.com")
				connections = append(connections, "https://*.geetest.com", "https://*.geevisit.com")
				images = append(images, "https://*.geetest.com", "https://*.geevisit.com")
				styles = append(styles, "https://*.geetest.com", "'unsafe-inline'")
				frames = append(frames, "https://*.geetest.com")
			case "tencent":
				scripts = append(scripts, "https://ssl.captcha.qq.com", "https://turing.captcha.qcloud.com", "https://turing.captcha.gtimg.com")
				frames = append(frames, "https://turing.captcha.qcloud.com", "https://turing.captcha.gtimg.com", "https://captcha.gtimg.com")
				connections = append(connections, "https://*.captcha.qq.com", "https://*.captcha.qcloud.com", "https://*.captcha.gtimg.com")
				images = append(images, "https://*.captcha.gtimg.com")
				styles = append(styles, "'unsafe-inline'")
			case "hcaptcha":
				scripts = append(scripts, "https://hcaptcha.com", "https://*.hcaptcha.com")
				frames = append(frames, "https://hcaptcha.com", "https://*.hcaptcha.com")
				connections = append(connections, "https://hcaptcha.com", "https://*.hcaptcha.com")
				styles = append(styles, "https://hcaptcha.com", "https://*.hcaptcha.com")
			default:
				scripts = append(scripts, "https://www.google.com/recaptcha/", "https://www.recaptcha.net/recaptcha/", "https://www.gstatic.com/recaptcha/")
				frames = append(frames, "https://www.google.com/recaptcha/", "https://www.recaptcha.net/recaptcha/", "https://recaptcha.google.com/recaptcha/")
				connections = append(connections, "https://www.google.com/recaptcha/", "https://www.recaptcha.net/recaptcha/")
			}
		}
		policy := w.Header().Get("Content-Security-Policy")
		for _, item := range []struct {
			name   string
			values []string
		}{{"script-src", scripts}, {"connect-src", connections}, {"style-src", styles}, {"img-src", images}} {
			if len(item.values) > 0 {
				policy = strings.Replace(policy, item.name+" 'self'", item.name+" 'self' "+strings.Join(item.values, " "), 1)
			}
		}
		if len(frames) > 0 {
			policy += "; frame-src 'self' " + strings.Join(frames, " ")
		}
		w.Header().Set("Content-Security-Policy", policy)
		next.ServeHTTP(w, r)
	})
}
