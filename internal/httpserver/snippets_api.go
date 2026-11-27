package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func (s *Server) registerSnippetRoutes(mux routeRegistrar) {
	mux.HandleFunc("POST /api/v1/admin/contents/{id}/snippet-draft/preview", s.adminSnippetDraftPreviewAPI)
	mux.HandleFunc("POST /api/v1/admin/contents/{id}/snippet-draft", s.adminSnippetDraftCreateAPI)
	for _, path := range []string{"snippet-draft", "snippet-draft/preview"} {
		for _, method := range []string{"GET", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			mux.HandleFunc(method+" /api/v1/admin/contents/{id}/"+path, s.adminAPIMethodNotAllowed("POST"))
		}
	}
	mux.HandleFunc("GET /admin/snippets/{$}", s.adminSnippets)
	mux.HandleFunc("GET /api/v1/admin/snippets", s.adminSnippetsListAPI)
	mux.HandleFunc("POST /api/v1/admin/snippets", s.adminSnippetSaveAPI)
	mux.HandleFunc("GET /api/v1/admin/snippets/{id}", s.adminSnippetItemAPI)
	mux.HandleFunc("PUT /api/v1/admin/snippets/{id}", s.adminSnippetSaveAPI)
	mux.HandleFunc("POST /api/v1/admin/snippets/{id}/withdraw", s.adminSnippetWithdrawAPI)
	mux.HandleFunc("GET /api/v1/admin/snippets/{id}/impacts", s.adminSnippetImpactsAPI)
	mux.HandleFunc("POST /api/v1/admin/snippets/{id}/preview", s.adminSnippetPreviewAPI)
	mux.HandleFunc("POST /api/v1/admin/snippets/{id}/apply", s.adminSnippetApplyAPI)
	for _, method := range []string{"PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/snippets", s.adminAPIMethodNotAllowed("GET, POST"))
	}
	for _, method := range []string{"POST", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/snippets/{id}", s.adminAPIMethodNotAllowed("GET, PUT"))
	}
	for _, action := range []string{"withdraw", "preview", "apply"} {
		for _, method := range []string{"GET", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			mux.HandleFunc(method+" /api/v1/admin/snippets/{id}/"+action, s.adminAPIMethodNotAllowed("POST"))
		}
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/snippets/{id}/impacts", s.adminAPIMethodNotAllowed("GET"))
	}
}

func snippetAccess(p auth.Principal) content.SnippetAccess {
	identity, _ := autosaveOwnerKey(p)
	return content.SnippetAccess{UserID: p.UserID, Identity: identity, Actor: p.Username, Read: p.Can(auth.CapabilityContentRead), Manage: p.Can(auth.CapabilityContentWrite), WriteOwn: p.Can(auth.CapabilityContentWriteOwn)}
}

func (s *Server) snippetPrincipal(w http.ResponseWriter, r *http.Request, write, manage bool) (content.SnippetAccess, bool) {
	if !s.requireAdminContentAPIEnabled(w, r) {
		return content.SnippetAccess{}, false
	}
	var p auth.Principal
	var ok bool
	if write {
		caps := []string{auth.CapabilityContentWrite}
		if !manage {
			caps = append(caps, auth.CapabilityContentWriteOwn)
		}
		p, ok = s.requireAdminJSONWrite(w, r, caps...)
	} else {
		p, _, ok = s.requireAdminJSONReadCapability(w, r, auth.CapabilityContentRead)
	}
	if !ok {
		return content.SnippetAccess{}, false
	}
	a := snippetAccess(p)
	if err := content.CheckSnippetAccess(a, write); err != nil {
		s.snippetError(w, r, err)
		return a, false
	}
	return a, true
}

func snippetETag(s content.Snippet) string { return fmt.Sprintf(`"snippet-%d-v%d"`, s.ID, s.Version) }
func (s *Server) snippetError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := 503, "unavailable", "片段暂时不可用，请稍后重试；当前输入仍保留"
	switch {
	case errors.Is(err, content.ErrNotFound):
		status, code, message = 404, "not_found", "片段或所选文章没有找到"
	case errors.Is(err, content.ErrSnippetPermission):
		status, code, message = 403, "permission_denied", "当前账号无权执行这个片段操作"
	case errors.Is(err, content.ErrSnippetMaintenance):
		status, code, message = 503, "maintenance", "共享片段或编辑回执正在维护，请保留当前输入并等待管理员处理"
	case errors.Is(err, content.ErrVersionConflict):
		status, code, message = 412, "version_conflict", "片段或文章已经变化，本次没有更新；请重新核对影响清单"
	case errors.Is(err, content.ErrSnippetReplay):
		status, code, message = 409, "request_conflict", "请求键已用于另一组更新，请核对原结果"
	case errors.Is(err, content.ErrSnippetCapacity):
		status, code, message = 409, "capacity", "片段、历史或操作回执已达到保留上限"
	default:
		if fields := content.FieldErrors(err); len(fields) > 0 {
			s.writeAPIError(w, r, 422, "snippets.invalid_input", "请检查输入内容", map[string]any{"fields": fields})
			return
		}
		slog.Error("snippet operation failed", "error", err)
	}
	s.writeAPIError(w, r, status, "snippets."+code, message, nil)
}

func (s *Server) snippetID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := positivePathID(r.PathValue("id"))
	if err != nil {
		s.snippetError(w, r, content.ErrNotFound)
		return 0, false
	}
	return id, true
}
func (s *Server) snippetPageQuery(w http.ResponseWriter, r *http.Request) (int, bool) {
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"page": true})
	if !ok {
		return 0, false
	}
	page, err := positiveQueryInt(q.Get("page"), 1, 100000)
	if err != nil {
		s.writeAPIError(w, r, 400, "snippets.invalid_query", "页码不正确", nil)
		return 0, false
	}
	return page, true
}
func (s *Server) snippetPrecondition(w http.ResponseWriter, r *http.Request, item content.Snippet) bool {
	values := r.Header.Values("If-Match")
	if len(values) != 1 || values[0] == "" {
		s.writeAPIError(w, r, 428, "snippets.precondition_required", "保存或撤回需要当前版本的 If-Match", nil)
		return false
	}
	if !strongETagMatches(values[0], snippetETag(item)) {
		s.snippetError(w, r, content.ErrVersionConflict)
		return false
	}
	return true
}

func (s *Server) adminSnippetsListAPI(w http.ResponseWriter, r *http.Request) {
	a, ok := s.snippetPrincipal(w, r, false, false)
	if !ok {
		return
	}
	page, ok := s.snippetPageQuery(w, r)
	if !ok {
		return
	}
	list, err := s.repository.ListSnippets(r.Context(), a, 24, (page-1)*24)
	if err != nil {
		s.snippetError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list.Items, "total": list.Total, "page": page, "pages": max(1, (list.Total+23)/24)})
}
func (s *Server) adminSnippetItemAPI(w http.ResponseWriter, r *http.Request) {
	a, ok := s.snippetPrincipal(w, r, false, false)
	if !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"view": true, "before": true})
	if !ok {
		return
	}
	id, ok := s.snippetID(w, r)
	if !ok {
		return
	}
	if q.Get("view") != "" && q.Get("view") != "summary" || q.Has("before") && q.Get("view") != "summary" {
		s.writeAPIError(w, r, 400, "snippets.invalid_query", "历史分页需要 view=summary", nil)
		return
	}
	if q.Get("view") == "summary" {
		before := int64(0)
		if q.Has("before") {
			var err error
			before, err = positivePathID(q.Get("before"))
			if err != nil {
				s.writeAPIError(w, r, 400, "snippets.invalid_query", "历史游标不正确", nil)
				return
			}
		}
		item, err := s.repository.GetSnippetPage(r.Context(), id, a, before, 24)
		if err != nil {
			s.snippetError(w, r, err)
			return
		}
		w.Header().Set("ETag", snippetETag(item.Snippet))
		writeJSON(w, 200, item)
		return
	}
	item, err := s.repository.GetSnippet(r.Context(), id, a)
	if err != nil {
		s.snippetError(w, r, err)
		return
	}
	w.Header().Set("ETag", snippetETag(item.Snippet))
	writeJSON(w, 200, item)
}

func (s *Server) adminSnippetSaveAPI(w http.ResponseWriter, r *http.Request) {
	a, ok := s.snippetPrincipal(w, r, true, true)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var current content.SnippetPage
	if r.PathValue("id") != "" {
		id, ok := s.snippetID(w, r)
		if !ok {
			return
		}
		var err error
		current, err = s.repository.GetSnippetPage(r.Context(), id, a, 0, 1)
		if err != nil {
			s.snippetError(w, r, err)
			return
		}
		if !s.snippetPrecondition(w, r, current.Snippet) {
			return
		}
	}
	var body struct {
		Title         apiOptionalString `json:"title"`
		ContentSource apiOptionalString `json:"content_source"`
		Visibility    apiOptionalString `json:"visibility"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 1<<20, "1 MiB"); !ok {
		return
	}
	item, err := s.repository.SaveSnippet(r.Context(), current.ID, content.Mutation{ExpectedVersion: current.Version, Actor: a.Actor}, content.SnippetInput{Title: body.Title.Value, ContentSource: body.ContentSource.Value, Visibility: body.Visibility.Value}, a)
	if err != nil {
		s.snippetError(w, r, err)
		return
	}
	status := 200
	if current.ID == 0 {
		status = 201
		w.Header().Set("Location", fmt.Sprintf("/api/v1/admin/snippets/%d", item.ID))
	}
	w.Header().Set("ETag", snippetETag(item))
	writeJSON(w, status, item)
}

func (s *Server) adminSnippetWithdrawAPI(w http.ResponseWriter, r *http.Request) {
	a, ok := s.snippetPrincipal(w, r, true, true)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	if !s.requireEmptyAPIBody(w, r) {
		return
	}
	id, ok := s.snippetID(w, r)
	if !ok {
		return
	}
	current, err := s.repository.GetSnippetPage(r.Context(), id, a, 0, 1)
	if err != nil {
		s.snippetError(w, r, err)
		return
	}
	if !s.snippetPrecondition(w, r, current.Snippet) {
		return
	}
	item, err := s.repository.WithdrawSnippet(r.Context(), id, content.Mutation{ExpectedVersion: current.Version, Actor: a.Actor}, a)
	if err != nil {
		s.snippetError(w, r, err)
		return
	}
	w.Header().Set("ETag", snippetETag(item))
	writeJSON(w, 200, item)
}

func (s *Server) adminSnippetImpactsAPI(w http.ResponseWriter, r *http.Request) {
	a, ok := s.snippetPrincipal(w, r, false, false)
	if !ok {
		return
	}
	if err := content.CheckSnippetAccess(a, true); err != nil {
		s.snippetError(w, r, err)
		return
	}
	id, ok := s.snippetID(w, r)
	if !ok {
		return
	}
	page, ok := s.snippetPageQuery(w, r)
	if !ok {
		return
	}
	items, err := s.repository.SnippetImpacts(r.Context(), id, a, 24, (page-1)*24)
	if err != nil {
		s.snippetError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"snippet": items.Snippet, "items": items.Items, "total": items.Total, "page": page, "pages": max(1, (items.Total+23)/24)})
}

func (s *Server) snippetUpdateInput(w http.ResponseWriter, r *http.Request) (content.SnippetUpdate, content.SnippetAccess, bool) {
	a, ok := s.snippetPrincipal(w, r, true, false)
	if !ok {
		return content.SnippetUpdate{}, a, false
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return content.SnippetUpdate{}, a, false
	}
	id, ok := s.snippetID(w, r)
	if !ok {
		return content.SnippetUpdate{}, a, false
	}
	var u content.SnippetUpdate
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &u, 32<<10, "32 KiB"); !ok {
		return u, a, false
	}
	if u.SnippetID != id {
		s.writeAPIError(w, r, 400, "snippets.id_mismatch", "片段编号与请求路径不一致", nil)
		return u, a, false
	}
	return u, a, true
}
func (s *Server) adminSnippetPreviewAPI(w http.ResponseWriter, r *http.Request) {
	u, a, ok := s.snippetUpdateInput(w, r)
	if !ok {
		return
	}
	plan, err := s.repository.PreviewSnippetUpdate(r.Context(), u, a)
	if err != nil {
		s.snippetError(w, r, err)
		return
	}
	writeJSON(w, 200, plan)
}
func (s *Server) adminSnippetApplyAPI(w http.ResponseWriter, r *http.Request) {
	u, a, ok := s.snippetUpdateInput(w, r)
	if !ok {
		return
	}
	result, err := s.repository.ApplySnippetUpdate(r.Context(), u, a)
	if err != nil {
		s.snippetError(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}
