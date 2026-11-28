package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/writingtemplate"
)

func (s *Server) SetWritingTemplates(service *writingtemplate.Service) {
	if service != nil {
		s.writingTemplates = service
	}
}

func (s *Server) registerWritingTemplateRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/v1/admin/ai/templates", s.adminWritingTemplates)
	mux.HandleFunc("POST /api/v1/admin/ai/templates", s.adminWritingTemplateWrite)
	mux.HandleFunc("GET /api/v1/admin/ai/templates/{id}", s.adminWritingTemplate)
	mux.HandleFunc("GET /api/v1/admin/ai/templates/{id}/export", s.adminWritingTemplateExport)
	mux.HandleFunc("POST /api/v1/admin/ai/template-import-preview", s.adminWritingTemplateImportPreview)
	for _, method := range []string{"PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/ai/templates", s.adminAPIMethodNotAllowed("GET, POST"))
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/ai/templates/{id}", s.adminAPIMethodNotAllowed("GET"))
		mux.HandleFunc(method+" /api/v1/admin/ai/templates/{id}/export", s.adminAPIMethodNotAllowed("GET"))
	}
	for _, method := range []string{"GET", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/ai/template-import-preview", s.adminAPIMethodNotAllowed("POST"))
	}
}

func (s *Server) writingTemplateError(w http.ResponseWriter, r *http.Request, err error) {
	if s.privateReadAuthError(w, r, err) {
		return
	}
	status, code, message := http.StatusServiceUnavailable, "unavailable", "模板结果暂时无法确认，请保留原请求并重试"
	switch {
	case errors.Is(err, writingtemplate.ErrInvalid):
		status, code, message = 400, "invalid", "请检查模板字段、版本和导入文件"
	case errors.Is(err, writingtemplate.ErrNotFound):
		status, code, message = 404, "not_found", "模板或历史版本不存在"
	case errors.Is(err, writingtemplate.ErrArchived):
		status, code, message = 409, "archived", "模板已归档，请重新选择；当前输入可继续保留"
	case errors.Is(err, writingtemplate.ErrConflict):
		status, code, message = 409, "conflict", "模板版本或请求内容已变化，请保留当前输入并重新核对"
	case errors.Is(err, writingtemplate.ErrCapacity):
		status, code, message = 409, "capacity", "模板或历史记录已达容量上限，已有内容仍保留"
	}
	s.writeAPIError(w, r, status, "ai.template_"+code, message, nil)
}

func (s *Server) adminWritingTemplates(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"limit": true, "offset": true})
	if !ok {
		return
	}
	limit, offset := 100, 0
	var err error
	if values.Has("limit") {
		limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil {
			s.writingTemplateError(w, r, writingtemplate.ErrInvalid)
			return
		}
	}
	if values.Has("offset") {
		offset, err = strconv.Atoi(values.Get("offset"))
		if err != nil {
			s.writingTemplateError(w, r, writingtemplate.ErrInvalid)
			return
		}
	}
	page, err := s.writingTemplates.List(r.Context(), true, limit, offset)
	if err != nil {
		s.writingTemplateError(w, r, err)
		return
	}
	s.writeWritingTemplateJSON(w, r, page)
}

func (s *Server) adminWritingTemplate(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	item, err := s.writingTemplates.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writingTemplateError(w, r, err)
		return
	}
	s.writeWritingTemplateJSON(w, r, item)
}

func (s *Server) adminWritingTemplateExport(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"version": true})
	if !ok {
		return
	}
	version, err := strconv.ParseInt(values.Get("version"), 10, 64)
	if err != nil || version < 1 {
		s.writingTemplateError(w, r, writingtemplate.ErrInvalid)
		return
	}
	raw, err := s.writingTemplates.Export(r.Context(), r.PathValue("id"), version)
	if err != nil {
		s.writingTemplateError(w, r, err)
		return
	}
	s.writeWritingTemplateBytes(w, r, raw, true)
}

func (s *Server) adminWritingTemplateImportPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		Document string `json:"document"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1<<20, "1 MiB"); !ok {
		return
	}
	r, _, ok := s.recheckWritingTemplateWrite(w, r)
	if !ok {
		return
	}
	preview, err := writingtemplate.PreviewImport([]byte(input.Document))
	if err != nil {
		s.writingTemplateError(w, r, err)
		return
	}
	s.writeWritingTemplateJSON(w, r, preview)
}

func (s *Server) adminWritingTemplateWrite(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	key, err := strictIdempotencyKey(r)
	if err != nil || key == "" {
		s.writeAPIError(w, r, 400, "ai.template_request_key", "请提供唯一的 Idempotency-Key，结果不确定时保留原键和原请求", nil)
		return
	}
	var input struct {
		Action          string                      `json:"action"`
		ID              string                      `json:"id"`
		ExpectedVersion int64                       `json:"expected_version"`
		Definition      *writingtemplate.Definition `json:"definition"`
		RestoreVersion  int64                       `json:"restore_version"`
		Document        string                      `json:"document"`
		ImportSHA256    string                      `json:"import_sha256"`
		ConfirmImport   bool                        `json:"confirm_import"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1<<20, "1 MiB"); !ok {
		return
	}
	if (input.Action == "import") != input.ConfirmImport {
		s.writingTemplateError(w, r, writingtemplate.ErrInvalid)
		return
	}
	r, p, ok = s.recheckWritingTemplateWrite(w, r)
	if !ok {
		return
	}
	hash := sha256.Sum256([]byte(key))
	actor := "user:" + strconv.FormatInt(p.UserID, 10)
	if p.UserID == 0 {
		h := sha256.Sum256([]byte(p.Username))
		actor = "bootstrap:" + hex.EncodeToString(h[:])
	}
	result, err := s.writingTemplates.ApplyAuthorized(r.Context(), writingtemplate.Command{Action: input.Action, ID: input.ID, ExpectedVersion: input.ExpectedVersion, Definition: input.Definition,
		RestoreVersion: input.RestoreVersion, Import: []byte(input.Document), ImportSHA256: input.ImportSHA256, RequestKey: hex.EncodeToString(hash[:]), Actor: actor},
		func() error { return s.adminReadAccess(r, auth.CapabilitySettingsManage) })
	if err != nil {
		s.writingTemplateError(w, r, err)
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 200, result)
}

func (s *Server) recheckWritingTemplateWrite(w http.ResponseWriter, r *http.Request) (*http.Request, auth.Principal, bool) {
	r, ok := s.freshSessionRequest(w, r)
	if !ok {
		return nil, auth.Principal{}, false
	}
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
	return r, p, ok
}

// Encode before refreshing authority, so large histories cannot retain the
// entry principal through storage and serialization. Export bytes are already
// encoded by the service and use the same output boundary.
func (s *Server) writeWritingTemplateJSON(w http.ResponseWriter, r *http.Request, value any) {
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(value); err != nil {
		s.writingTemplateError(w, r, err)
		return
	}
	s.writeWritingTemplateBytes(w, r, encoded.Bytes(), false)
}

func (s *Server) writeWritingTemplateBytes(w http.ResponseWriter, r *http.Request, raw []byte, attachment bool) {
	r, ok := s.recheckAdminRead(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	if attachment {
		w.Header().Set("Content-Disposition", `attachment; filename="writing-template.json"`)
	}
	if _, err := privateResponse(w, r, func() error { return s.adminReadAccess(r, auth.CapabilitySettingsManage) }).Write(raw); err != nil {
		panic(http.ErrAbortHandler)
	}
}
