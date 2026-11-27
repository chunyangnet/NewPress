package httpserver

import (
	"errors"
	"net/http"
	"regexp"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/update"
)

func (s *Server) SetUpdates(manager *update.Manager) { s.updates = manager }

func (s *Server) updateAvailable(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if s.updates == nil {
		s.writeAPIError(w, r, http.StatusServiceUnavailable, "updates.unavailable", "更新服务尚未初始化", nil)
		return false
	}
	return true
}

func (s *Server) adminUpdates(w http.ResponseWriter, r *http.Request) {
	p, ro, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySystemUpdate)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	data := s.baseViewFor(w, "系统更新", "从官方公开双源或私有发布源安全更新 NewPress", "/admin/updates/")
	data.BodyClass = "is-admin is-admin-updates"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("updates", p, ro)
	if s.updates != nil {
		snapshot, err := s.updates.Snapshot()
		if err != nil {
			s.renderError(w, 503, "无法读取更新配置或任务状态")
			return
		}
		data.Updates = snapshot
	}
	s.render(w, http.StatusOK, "admin-updates", data)
}

func (s *Server) updateWrite(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySystemUpdate); !ok {
		return false
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return false
	}
	return s.updateAvailable(w, r)
}

func (s *Server) updateError(w http.ResponseWriter, r *http.Request, err error) {
	code := http.StatusUnprocessableEntity
	if errors.Is(err, update.ErrBusy) {
		code = http.StatusConflict
	}
	s.writeAPIError(w, r, code, "updates.rejected", err.Error(), nil)
}

func (s *Server) adminUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemUpdate); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	if !s.updateAvailable(w, r) {
		return
	}
	value, err := s.updates.Snapshot()
	if err != nil {
		s.updateError(w, r, err)
		return
	}
	writeJSON(w, 200, value)
}
func (s *Server) adminUpdateConfigure(w http.ResponseWriter, r *http.Request) {
	if !s.updateWrite(w, r) {
		return
	}
	var input struct {
		Official   bool   `json:"official"`
		Owner      string `json:"owner"`
		Repo       string `json:"repo"`
		Ref        string `json:"ref"`
		Manifest   string `json:"manifest"`
		Token      string `json:"token"`
		ClearToken bool   `json:"clear_token"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 4096, "4 KiB"); !ok {
		return
	}
	if err := s.updates.Configure(update.Source{Official: input.Official, Owner: input.Owner, Repo: input.Repo, Ref: input.Ref, Manifest: input.Manifest, Token: input.Token}, input.ClearToken); err != nil {
		s.updateError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"saved": true})
}

var updatePolicyRevision = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (s *Server) adminUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	if !s.updateWrite(w, r) {
		return
	}
	var input map[string]any
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1024, "1 KiB"); !ok {
		return
	}
	revision, _ := input["revision"].(string)
	if !updatePolicyRevision.MatchString(revision) {
		s.writeAPIError(w, r, 400, "updates.policy_revision_required", "请先载入当前更新策略", nil)
		return
	}
	var next *update.Policy
	if reset, present := input["use_defaults"]; present {
		if reset != true || len(input) != 2 {
			s.writeAPIError(w, r, 400, "updates.invalid_policy", "恢复部署默认值时请仅提交 revision 和 use_defaults: true", nil)
			return
		}
	} else {
		auto, hasAuto := input["auto_check"].(bool)
		interval, hasInterval := input["check_interval_minutes"].(float64)
		window, hasWindow := input["observation_seconds"].(float64)
		if len(input) != 4 || !hasAuto || !hasInterval || !hasWindow {
			s.writeAPIError(w, r, 400, "updates.invalid_policy", "请完整填写自动检查、检查间隔和健康观察时长", nil)
			return
		}
		next = &update.Policy{AutoCheck: auto, CheckIntervalMinutes: interval, ObservationSeconds: window}
	}
	saved, err := s.updates.ConfigurePolicy(revision, next)
	if errors.Is(err, update.ErrPolicyConflict) {
		s.writeAPIError(w, r, http.StatusConflict, "updates.policy_changed", err.Error(), nil)
		return
	}
	if err != nil {
		s.updateError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) adminUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !s.updateWrite(w, r) || !s.requireEmptyAPIBody(w, r) {
		return
	}
	if err := s.updates.Check(); err != nil {
		s.updateError(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"accepted": true})
}
func (s *Server) adminUpdateStart(w http.ResponseWriter, r *http.Request) {
	if !s.updateWrite(w, r) {
		return
	}
	var input struct {
		Version         string `json:"version"`
		BackupConfirmed bool   `json:"backup_confirmed"`
		Revision        string `json:"revision"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1024, "1 KiB"); !ok {
		return
	}
	s.versionMu.Lock()
	defer s.versionMu.Unlock()
	if s.extensions != nil && s.extensions.Busy() {
		s.updateError(w, r, update.ErrBusy)
		return
	}
	ticket, err := s.updates.Start(input.Version, input.BackupConfirmed, input.Revision)
	if err != nil {
		s.updateError(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]string{"ticket": ticket})
}
func (s *Server) adminUpdateCancel(w http.ResponseWriter, r *http.Request) {
	if !s.updateWrite(w, r) || !s.requireEmptyAPIBody(w, r) {
		return
	}
	if err := s.updates.Cancel(); err != nil {
		s.updateError(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]bool{"accepted": true})
}
func (s *Server) adminUpdateProgress(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	if !s.updateAvailable(w, r) {
		return
	}
	// A short-lived, job-scoped receipt survives the session reset at restart.
	// It grants only sanitized progress, never repository access or mutations.
	if state, ok := s.updates.Progress(r.Header.Get("X-Update-Ticket")); ok {
		writeJSON(w, 200, state)
		return
	}
	s.writeAPIError(w, r, http.StatusUnauthorized, "updates.invalid_ticket", "进度凭据已过期，请重新登录查看结果", nil)
}
