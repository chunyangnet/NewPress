package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/objectstorage"
	"newpress.local/newpress/internal/plugin"
)

type storageAdminView struct {
	Status    objectstorage.Status
	StateJSON string
	Groups    []plugin.SettingsGroup
}

func (s *Server) SetObjectStorage(service *objectstorage.Service) { s.objectStorage = service }

func (s *Server) registerObjectStorageRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/plugins/object-storage/{$}", s.adminObjectStorage)
	mux.HandleFunc("GET /api/v1/admin/plugins/object-storage/status", s.objectStorageStatus)
	mux.HandleFunc("GET /api/v1/admin/plugins/object-storage/records", s.objectStorageRecords)
	mux.HandleFunc("GET /api/v1/admin/plugins/object-storage/files", s.objectStorageFiles)
	mux.HandleFunc("POST /api/v1/admin/plugins/object-storage/files/action", s.objectStorageFileAction)
	mux.HandleFunc("POST /api/v1/admin/plugins/object-storage/files/upload", s.objectStorageFileUpload)
	mux.HandleFunc("PUT /api/v1/admin/plugins/object-storage/settings", s.objectStorageSave)
	mux.HandleFunc("POST /api/v1/admin/plugins/object-storage/action", s.objectStorageAction)
}

func (s *Server) adminObjectStorage(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{}); !ok {
		return
	}
	if s.objectStorage == nil {
		s.renderError(w, http.StatusServiceUnavailable, "对象存储服务暂不可用")
		return
	}
	var state objectstorage.Status
	if readOnly {
		// The unauthenticated local preview must not reveal configured accounts,
		// endpoints, cloud object keys or any existing synchronization history.
		state.Settings.Settings = objectstorage.Defaults()
		state.UploadEntries = objectstorage.UploadEntries()
		state.Counts = map[string]int{}
	} else {
		var err error
		state, err = s.objectStorage.Status()
		if err != nil {
			s.renderError(w, http.StatusNotFound, "请先安装对象存储插件")
			return
		}
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, "无法读取存储设置")
		return
	}
	data := s.baseViewFor(w, "对象存储", "管理云端媒体副本、同步和访问方式。", "/admin/plugins/object-storage/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-object-storage"
	data.Admin = s.adminView("plugins", p, readOnly)
	data.Admin.DetailTitle, data.Admin.PluginSlug = "对象存储", plugin.ObjectStorage
	data.ObjectStorage = storageAdminView{Status: state, StateJSON: string(encoded), Groups: []plugin.SettingsGroup{
		{ID: "connections", Title: "存储连接", Icon: "database"},
		{ID: "files", Title: "云端文件", Icon: "database"},
		{ID: "policy", Title: "同步与访问", Icon: "sliders-horizontal"},
		{ID: "routes", Title: "上传入口", Icon: "sliders-horizontal"},
		{ID: "operations", Title: "同步任务", Icon: "refresh-cw"},
		{ID: "migration", Title: "存储迁移", Icon: "database"},
		{ID: "records", Title: "运行记录", Icon: "activity"},
	}}
	s.render(w, http.StatusOK, "admin-object-storage", data)
}

func (s *Server) objectStorageStatus(w http.ResponseWriter, r *http.Request) {
	_, readOnly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	if readOnly {
		s.writeAPIError(w, r, http.StatusForbidden, "storage.authentication", "请登录后查看存储配置与运行记录", nil)
		return
	}
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{}); !ok {
		return
	}
	if s.objectStorage == nil {
		s.writeAPIError(w, r, http.StatusServiceUnavailable, "storage.unavailable", "对象存储服务暂不可用", nil)
		return
	}
	state, err := s.objectStorage.Status()
	if err != nil {
		s.objectStorageError(w, r, err, false)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) objectStorageSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	var body struct {
		Settings       objectstorage.Settings `json:"settings"`
		ClearTokens    []string               `json:"clear_tokens"`
		ExpectedUserID int64                  `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 128<<10, "128 KiB"); !ok || !s.pluginIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	if s.objectStorage == nil {
		s.writeAPIError(w, r, http.StatusServiceUnavailable, "storage.unavailable", "对象存储服务暂不可用", nil)
		return
	}
	if len(body.ClearTokens) > 8 {
		s.writeAPIError(w, r, http.StatusBadRequest, "storage.invalid", "清除令牌列表超出范围", nil)
		return
	}
	settings, err := s.objectStorage.Save(body.Settings, body.ClearTokens)
	if err != nil {
		s.objectStorageError(w, r, err, true)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": settings})
}

func (s *Server) objectStorageAction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	var body struct {
		Action          string `json:"action"`
		ProfileID       string `json:"profile_id"`
		ExpectedVersion int64  `json:"expected_version"`
		ExpectedUserID  int64  `json:"expected_user_id"`
		ConfirmCleanup  bool   `json:"confirm_cleanup"`
		TargetProfileID string `json:"target_profile_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 4096, "4 KiB"); !ok || !s.pluginIdentity(w, r, p, body.ExpectedUserID) {
		return
	}
	if s.objectStorage == nil {
		s.writeAPIError(w, r, http.StatusServiceUnavailable, "storage.unavailable", "对象存储服务暂不可用", nil)
		return
	}
	if body.ProfileID == "" || len(body.ProfileID) > 40 {
		s.writeAPIError(w, r, http.StatusBadRequest, "storage.profile", "请选择已保存的存储配置", nil)
		return
	}
	if allowed, _ := s.accessRate.allow("storage-action:"+strconv.FormatInt(p.UserID, 10), 15, time.Now()); !allowed {
		s.writeAPIError(w, r, http.StatusTooManyRequests, "storage.rate", "操作过于频繁，请稍后重试", nil)
		return
	}
	state, err := s.objectStorage.Status()
	if err != nil {
		s.objectStorageError(w, r, err, false)
		return
	}
	if body.ExpectedVersion < 1 || body.ExpectedVersion != state.Settings.Version {
		s.objectStorageError(w, r, objectstorage.ErrConflict, false)
		return
	}
	var count int
	switch body.Action {
	case "preview_migration", "migrate":
		preview, migrationErr := s.objectStorage.Migrate(body.ProfileID, body.TargetProfileID, body.ExpectedVersion, body.Action == "migrate")
		if migrationErr != nil {
			s.objectStorageError(w, r, migrationErr, false)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "migration": preview})
		return
	case "test":
		result, testErr := s.objectStorage.Test(r.Context(), body.ProfileID, body.ExpectedVersion)
		if testErr != nil {
			if errors.Is(testErr, objectstorage.ErrConflict) || errors.Is(testErr, plugin.ErrInactive) {
				s.objectStorageError(w, r, testErr, false)
				return
			}
			s.writeAPIError(w, r, http.StatusUnprocessableEntity, "storage.test", objectstorage.PublicError(testErr), map[string]any{"probe": result})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "probe": result})
		return
	case "sync":
		count, err = s.objectStorage.Synchronize(body.ProfileID, body.ExpectedVersion)
	case "retry":
		count, err = s.objectStorage.Retry(body.ProfileID, body.ExpectedVersion)
	case "verify":
		count, err = s.objectStorage.Verify(body.ProfileID, body.ExpectedVersion)
	case "cleanup":
		if !body.ConfirmCleanup {
			s.writeAPIError(w, r, http.StatusBadRequest, "storage.confirmation", "请先核对待清理对象，再确认清理云端副本", nil)
			return
		}
		count, err = s.objectStorage.Cleanup(body.ProfileID, body.ExpectedVersion)
	default:
		s.writeAPIError(w, r, http.StatusBadRequest, "storage.action", "不支持该存储操作", nil)
		return
	}
	if err != nil {
		s.objectStorageError(w, r, err, false)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scheduled": count})
}

func (s *Server) objectStorageRecords(w http.ResponseWriter, r *http.Request) {
	_, readOnly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	if readOnly {
		s.writeAPIError(w, r, http.StatusForbidden, "storage.authentication", "请登录后查看运行记录", nil)
		return
	}
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{"profile": true, "status": true, "q": true, "page": true}); !ok {
		return
	}
	if s.objectStorage == nil {
		s.writeAPIError(w, r, http.StatusServiceUnavailable, "storage.unavailable", "对象存储服务暂不可用", nil)
		return
	}
	q := r.URL.Query()
	page := 1
	var err error
	if q.Get("page") != "" {
		page, err = strconv.Atoi(q.Get("page"))
	}
	if err != nil || page < 1 || len(q.Get("q")) > 200 || len(q.Get("profile")) > 40 || len(q.Get("status")) > 20 {
		s.writeAPIError(w, r, http.StatusBadRequest, "storage.query", "记录查询参数无效", nil)
		return
	}
	result, err := s.objectStorage.Records(q.Get("profile"), q.Get("status"), q.Get("q"), page)
	if err != nil {
		s.objectStorageError(w, r, err, false)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) objectStorageError(w http.ResponseWriter, r *http.Request, err error, validation bool) {
	if errors.Is(err, auth.ErrSessionUnavailable) {
		s.sessionStorageError(w, r)
		return
	}
	status, code, message := http.StatusUnprocessableEntity, "storage.operation", objectstorage.PublicError(err)
	if validation {
		message = err.Error()
	}
	if errors.Is(err, objectstorage.ErrConflict) {
		status, code, message = http.StatusConflict, "storage.conflict", objectstorage.ErrConflict.Error()
	}
	if errors.Is(err, plugin.ErrInactive) {
		status, code, message = http.StatusNotFound, "storage.inactive", "对象存储插件尚未安装或状态已变化"
	}
	if errors.Is(err, auth.ErrSessionNotFound) {
		w.Header().Set("WWW-Authenticate", `NewPressSession realm="admin"`)
		status, code, message = http.StatusUnauthorized, "auth.authentication_required", "需要有效的管理员会话"
	}
	if errors.Is(err, auth.ErrSessionForbidden) {
		status, code, message = http.StatusForbidden, "auth.capability_required", "当前账号没有管理云端文件的权限"
	}
	s.writeAPIError(w, r, status, code, message, nil)
}
