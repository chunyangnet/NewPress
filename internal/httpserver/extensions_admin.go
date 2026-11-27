package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"os"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/extensionupdate"
)

type extensionUpdateView struct {
	extensionupdate.Snapshot
	Title, BackURL string
	Marketplace    bool
	AutoCheck      bool
}

func (s *Server) SetExtensionUpdates(service *extensionupdate.Service) { s.extensions = service }

func (s *Server) registerExtensionUpdateRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/themes/{$}", s.adminThemes)
	mux.HandleFunc("GET /api/v1/admin/themes/{slug}/cover", s.themeCover)
	mux.HandleFunc("GET /admin/plugins/updates/{$}", func(w http.ResponseWriter, r *http.Request) { s.adminExtensionUpdates(w, r, "plugin") })
	mux.HandleFunc("GET /admin/appearance/updates/{$}", func(w http.ResponseWriter, r *http.Request) { s.adminExtensionUpdates(w, r, "theme") })
	mux.HandleFunc("GET /api/v1/admin/extensions/{kind}", s.extensionSnapshot)
	mux.HandleFunc("GET /api/v1/admin/extensions/{kind}/job", s.extensionJob)
	mux.HandleFunc("POST /api/v1/admin/extensions/{kind}/check", func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("action", "check")
		s.extensionStart(w, r)
	})
	mux.HandleFunc("POST /api/v1/admin/extensions/{kind}/cancel", s.extensionCancel)
	mux.HandleFunc("POST /api/v1/admin/extensions/{kind}/{slug}/{action}", s.extensionStart)
}

func extensionCapability(kind string) string {
	switch kind {
	case "plugin":
		return auth.CapabilityPluginsManage
	case "theme":
		return auth.CapabilityAppearanceManage
	default:
		return ""
	}
}

func (s *Server) extensionAvailable(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if s.extensions == nil {
		s.writeAPIError(w, r, 503, "extensions.unavailable", "扩展更新服务尚未初始化", nil)
		return false
	}
	return true
}

func (s *Server) extensionChannel(w http.ResponseWriter, r *http.Request) (string, bool) {
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"channel": true})
	if !ok {
		return "", false
	}
	channel := q.Get("channel")
	if channel == "" {
		channel = "stable"
	}
	if channel != "stable" && channel != "preview" {
		s.writeAPIError(w, r, 400, "extensions.invalid_channel", "请选择稳定或预览通道", nil)
		return "", false
	}
	return channel, true
}

func (s *Server) adminExtensionUpdates(w http.ResponseWriter, r *http.Request, kind string) {
	p, ro, ok := s.requireAdminPageCapability(w, r, extensionCapability(kind))
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	channel, ok := s.extensionChannel(w, r)
	if !ok {
		return
	}
	title, back, section := "插件版本与更新", "/admin/plugins/", "plugins"
	if kind == "theme" {
		title, back, section = "主题版本与更新", "/admin/themes/", "themes"
	}
	data := s.baseViewFor(w, title, "检查发行版本，在线更新或回退扩展。", r.URL.Path)
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-extension-updates"
	data.Admin = s.adminView(section, p, ro)
	view := extensionUpdateView{Title: title, BackURL: back, Snapshot: extensionupdate.Snapshot{Kind: kind, Channel: channel}}
	if s.extensions != nil {
		snapshot, err := s.extensions.Snapshot(kind, channel)
		if err != nil {
			s.renderError(w, 503, "无法读取扩展版本")
			return
		}
		view.Snapshot = snapshot
	}
	data.ExtensionUpdates = view
	s.render(w, http.StatusOK, "admin-extension-updates", data)
}

func (s *Server) extensionRead(w http.ResponseWriter, r *http.Request) bool {
	capability := extensionCapability(r.PathValue("kind"))
	if capability == "" {
		s.writeAPIError(w, r, 404, "extensions.not_found", "扩展类型不存在", nil)
		return false
	}
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, capability); !ok {
		return false
	}
	return s.extensionAvailable(w, r)
}

func (s *Server) extensionSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.extensionRead(w, r) {
		return
	}
	channel, ok := s.extensionChannel(w, r)
	if !ok {
		return
	}
	snapshot, err := s.extensions.Snapshot(r.PathValue("kind"), channel)
	if err != nil {
		s.extensionError(w, r, err)
		return
	}
	// The raw signed catalog is public, but this projection still includes only
	// the extension kind authorized by this route.
	if r.PathValue("kind") == "plugin" {
		snapshot.Market.Themes = nil
	} else {
		snapshot.Market.Plugins = nil
	}
	writeJSON(w, 200, snapshot)
}

func (s *Server) extensionJob(w http.ResponseWriter, r *http.Request) {
	if !s.extensionRead(w, r) {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	writeJSON(w, 200, s.extensions.Job(r.PathValue("kind")))
}

func (s *Server) extensionWrite(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	capability := extensionCapability(r.PathValue("kind"))
	if capability == "" {
		s.writeAPIError(w, r, 404, "extensions.not_found", "扩展类型不存在", nil)
		return auth.Principal{}, false
	}
	p, ok := s.requireAdminJSONWrite(w, r, capability)
	if !ok {
		return p, false
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return p, false
	}
	return p, s.extensionAvailable(w, r)
}

func (s *Server) extensionStart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.extensionWrite(w, r)
	if !ok {
		return
	}
	var body struct {
		Version         string `json:"version"`
		Digest          string `json:"digest"`
		ExpectedVersion string `json:"expected_version"`
		Revision        string `json:"revision"`
		Channel         string `json:"channel"`
		ExpectedUserID  int64  `json:"expected_user_id"`
		ExpectedActive  string `json:"expected_active"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &body, 4096, "4 KiB"); !ok {
		return
	}
	if !s.extensionIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	if body.Channel == "" {
		body.Channel = "stable"
	}
	s.versionMu.Lock()
	defer s.versionMu.Unlock()
	if s.updates != nil {
		state, err := s.updates.Snapshot()
		if err != nil {
			s.extensionError(w, r, err)
			return
		}
		if state.State.BlocksChanges() {
			s.extensionError(w, r, extension.ErrBusy)
			return
		}
	}
	request := extensionupdate.Request{
		Kind: r.PathValue("kind"), Action: r.PathValue("action"), Slug: r.PathValue("slug"),
		Version: body.Version, Digest: body.Digest, ExpectedVersion: body.ExpectedVersion, Revision: body.Revision, Channel: body.Channel,
		ExpectedActive: body.ExpectedActive,
	}
	job, err := s.extensions.Start(request)
	if err != nil {
		s.extensionError(w, r, err)
		return
	}
	slog.Info("extension task started", "kind", request.Kind, "action", request.Action, "slug", request.Slug, "version", request.Version, "actor", p.Username)
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) extensionIdentity(w http.ResponseWriter, r *http.Request, p auth.Principal, expected int64) bool {
	if expected != p.UserID {
		s.writeAPIError(w, r, 409, "extensions.identity_changed", "登录账号已变化，请刷新并核对账号后重试", nil)
		return false
	}
	return true
}

func (s *Server) extensionCancel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.extensionWrite(w, r)
	if !ok {
		return
	}
	var body struct {
		ID             string `json:"id"`
		ExpectedUserID int64  `json:"expected_user_id"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &body, 1024, "1 KiB"); !ok {
		return
	}
	if !s.extensionIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	if err := s.extensions.Cancel(r.PathValue("kind"), body.ID); err != nil {
		s.extensionError(w, r, err)
		return
	}
	writeJSON(w, 202, s.extensions.Job(r.PathValue("kind")))
}

func (s *Server) extensionError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := http.StatusUnprocessableEntity, err.Error()
	if errors.Is(err, extension.ErrConflict) || errors.Is(err, extension.ErrBusy) {
		status = http.StatusConflict
	}
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		slog.Warn("extension filesystem operation failed", "error", err)
		status, message = http.StatusServiceUnavailable, "无法读写扩展更新状态，请检查服务日志"
	}
	s.writeAPIError(w, r, status, "extensions.rejected", message, nil)
}
