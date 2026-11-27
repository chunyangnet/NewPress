package httpserver

import (
	"errors"
	"net/http"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

func (s *Server) registerRegistrationCodeRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/registration-codes/{$}", s.adminRegistrationCodePage)
	for _, route := range []struct {
		path, methods string
		get, post     http.HandlerFunc
	}{
		{"/api/v1/admin/registration-codes", "GET, POST", s.adminRegistrationCodes, s.adminRegistrationCodeMutation},
		{"/api/v1/admin/registration-codes/policy", "POST", nil, s.adminRegistrationCodePolicy},
	} {
		for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			h := s.adminAPIMethodNotAllowed(route.methods)
			if method == "GET" && route.get != nil {
				h = route.get
			}
			if method == "POST" && route.post != nil {
				h = route.post
			}
			mux.HandleFunc(method+" "+route.path, h)
		}
	}
}

func (s *Server) adminRegistrationCodePage(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	data := s.baseViewFor(w, "注册码工作台", "管理注册准入、使用次数与有效期", "/admin/registration-codes/")
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-registration-codes"
	data.Admin = s.adminView("registration-codes", p, readOnly)
	s.render(w, 200, "admin-registration-codes", data)
}

func registrationCodePolicyView(snap site.SettingsSnapshot) map[string]any {
	p := snap.Settings.RegistrationPolicy()
	return map[string]any{"revision": snap.Revision, "mode": p.Fields.RegistrationCode, "registration": snap.Settings.AccountRegistration(), "code_url": p.RegistrationCodeURL}
}

func (s *Server) adminRegistrationCodes(w http.ResponseWriter, r *http.Request) {
	_, readOnly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if readOnly {
		s.writeAPIError(w, r, 403, "auth.authentication_required", "登录管理员账号后可查看注册码", nil)
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	items, err := s.auth.ListRegistrationCodes(r.Context())
	if err != nil {
		s.registrationCodeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"items": items, "policy": registrationCodePolicyView(s.siteSettings.Snapshot()), "now": time.Now().UTC(), "max_codes": auth.MaxRegistrationCodes, "max_batch": auth.MaxRegistrationCodeBatch})
}

func (s *Server) adminRegistrationCodeMutation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input auth.RegistrationCodeMutation
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &input, 256<<10, "256 KiB"); !ok {
		return
	}
	items, err := s.auth.MutateRegistrationCodes(r.Context(), input, p.Username)
	if err != nil {
		s.registrationCodeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) adminRegistrationCodePolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		Revision string `json:"revision"`
		Mode     string `json:"mode"`
		CodeURL  string `json:"code_url"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 4096, "4 KiB"); !ok {
		return
	}
	snap := s.siteSettings.Snapshot()
	settings := snap.Settings
	p := settings.RegistrationPolicy()
	p.Fields.RegistrationCode = input.Mode
	p.RegistrationCodeURL = input.CodeURL
	settings.Accounts = &site.AccountSettings{Registration: settings.AccountRegistration(), Policy: &p}
	saved, fields, err := s.siteSettings.Save(input.Revision, settings)
	if errors.Is(err, site.ErrRevisionConflict) || errors.Is(err, site.ErrRevisionRequired) {
		s.writeAPIError(w, r, 409, "registration_codes.policy_conflict", "注册设置已变更，请刷新后重试", nil)
		return
	}
	if len(fields) > 0 {
		s.registrationCodeError(w, r, &auth.UserValidationError{Fields: fields})
		return
	}
	if err != nil {
		s.registrationCodeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, registrationCodePolicyView(saved))
}

func (s *Server) registrationCodeError(w http.ResponseWriter, r *http.Request, err error) {
	if fields := auth.UserFieldErrors(err); len(fields) > 0 {
		s.writeAPIError(w, r, 422, "registration_codes.invalid_input", "请检查注册码设置", map[string]any{"fields": fields})
		return
	}
	if errors.Is(err, auth.ErrRegistrationCodeConflict) {
		s.writeAPIError(w, r, 409, "registration_codes.conflict", err.Error(), nil)
		return
	}
	if errors.Is(err, auth.ErrRegistrationCodeLimit) {
		s.writeAPIError(w, r, 422, "registration_codes.limit", err.Error(), nil)
		return
	}
	s.writeAPIError(w, r, 503, "registration_codes.unavailable", "注册码暂时无法读取或保存，请稍后重试", nil)
}
