package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func (s *Server) adminSessionAPI(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminJSONRead(writer, request)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	contentAPIEnabled := s.siteSettings.Snapshot().Settings.API.AdminContentEnabled
	capabilities := make([]string, 0, 18)
	if contentAPIEnabled && (readOnly || principal.Can(auth.CapabilityContentRead)) {
		capabilities = append(capabilities, "content.read")
	}
	if !readOnly && contentAPIEnabled && canCreateContent(principal) {
		capabilities = append(capabilities,
			"content.create", "content.update", "content.publish", "content.trash", "content.revisions.restore",
		)
	}
	if readOnly || principal.Can(auth.CapabilityCommentsModerate) {
		capabilities = append(capabilities, "comments.read", "comments.revisions.read")
	}
	if !readOnly && principal.Can(auth.CapabilityCommentsModerate) {
		capabilities = append(capabilities,
			"comments.moderate", "comments.edit", "comments.reply", "comments.revisions.restore", "comments.delete",
		)
	}
	if readOnly || principal.Can(auth.CapabilitySessionsRead) {
		capabilities = append(capabilities, "sessions.read")
	}
	if readOnly || principal.Can(auth.CapabilitySystemRead) {
		capabilities = append(capabilities, "audit.read")
	}
	if readOnly || principal.Can(auth.CapabilitySystemUpdate) {
		capabilities = append(capabilities, "updates.read")
	}
	if !readOnly && principal.Can(auth.CapabilitySystemUpdate) {
		capabilities = append(capabilities, "updates.configure", "updates.check", "updates.install", "updates.cancel")
	}
	if !readOnly && principal.Can(auth.CapabilitySessionsManage) {
		capabilities = append(capabilities, "sessions.revoke", "sessions.revoke.own")
	} else if !readOnly && principal.Can(auth.CapabilitySessionsOwn) {
		capabilities = append(capabilities, "sessions.revoke.own")
	}
	response := adminSessionAPIResponse{
		Authenticated:    principal.Username != "",
		ReadOnly:         readOnly,
		UserID:           principal.UserID,
		Username:         principal.Username,
		DisplayName:      principal.Label(),
		Role:             principal.RoleName,
		Roles:            append([]string(nil), principal.RoleSlugs...),
		CSRFToken:        principal.CSRFToken,
		Capabilities:     capabilities,
		RoleCapabilities: append([]string(nil), principal.Capabilities...),
	}
	if !principal.ExpiresAt.IsZero() {
		expiresAt := principal.ExpiresAt
		response.ExpiresAt = &expiresAt
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) adminContentsAPI(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAdminContentAPIEnabled(writer, request) {
		return
	}
	principal, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityContentRead)
	if !ok {
		return
	}
	queryValues, ok := s.strictAPIQuery(writer, request, map[string]bool{
		"type": true, "status": true, "q": true, "page": true, "per_page": true,
	})
	if !ok {
		return
	}
	page, err := positiveQueryInt(queryValues.Get("page"), 1, 1_000_000)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "page 必须是有效的正整数", map[string]any{"field": "page"})
		return
	}
	defaultPageSize := s.siteSettings.Snapshot().Settings.Reading.AdminPageSize
	perPage, err := positiveQueryInt(queryValues.Get("per_page"), defaultPageSize, adminContentAPIMaxPageSize)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "per_page 必须介于 1 和 100 之间", map[string]any{"field": "per_page"})
		return
	}
	query, err := content.NormalizeAdminQuery(content.AdminQuery{
		AuthorID: contentAuthorScope(principal),
		Type:     queryValues.Get("type"), Status: queryValues.Get("status"), Search: queryValues.Get("q"),
		Limit: perPage, Offset: (page - 1) * perPage,
	})
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "content.invalid_filter", "内容筛选条件不正确", nil)
		return
	}
	result, err := s.repository.ListAdmin(request.Context(), query)
	if err != nil {
		slog.Error("list content API", "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "content.unavailable", "内容列表暂时不可用", nil)
		return
	}
	now := time.Now()
	items := make([]adminContentSummaryAPIResponse, 0, len(result.Items))
	for _, post := range result.Items {
		items = append(items, adminContentSummaryResponse(post, now, s.contentPath))
	}
	pages := 0
	if result.Total > 0 {
		pages = (result.Total + perPage - 1) / perPage
	}
	pagination := adminContentPaginationAPIResponse{Page: page, PerPage: perPage, Total: result.Total, Pages: pages}
	if page > 1 {
		pagination.Previous = adminContentsAPIURL(query, page-1, perPage, defaultPageSize)
	}
	if page < pages {
		pagination.Next = adminContentsAPIURL(query, page+1, perPage, defaultPageSize)
	}
	writeJSON(writer, http.StatusOK, adminContentsAPIResponse{
		Items: items, Pagination: pagination, StatusCounts: result.StatusCounts, TypeCounts: result.TypeCounts,
	})
}

func (s *Server) adminContentAPI(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAdminContentAPIEnabled(writer, request) {
		return
	}
	principal, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityContentRead)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	post, ok := s.loadAdminContentAPI(writer, request)
	if !ok {
		return
	}
	if !s.requireContentAPIObjectRead(writer, request, principal, post) {
		return
	}
	s.setContentAPIHeaders(writer, post)
	if etagMatches(request.Header.Get("If-None-Match"), contentAPIETag(post)) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(writer, http.StatusOK, adminContentResponse(post, time.Now(), s.contentPath))
}

func (s *Server) adminContentCreateAPI(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAdminContentAPIEnabled(writer, request) {
		return
	}
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	idempotencyKey, err := strictIdempotencyKey(request)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_idempotency_key", "Idempotency-Key 必须是 8–128 位字母、数字或 . _ : -", nil)
		return
	}
	var mutation adminContentMutationAPIRequest
	body, ok := s.decodeStrictAPIJSON(writer, request, &mutation)
	if !ok {
		return
	}
	if idempotencyKey != "" {
		s.createContentAPISubmission(writer, request, principal, mutation, body, idempotencyKey)
		return
	}
	input, fields := createAPIInput(mutation, principal, time.Now(), s.siteSettings.Snapshot().Settings.Writing)
	if len(fields) > 0 {
		s.writeContentValidationAPIError(writer, request, fields)
		return
	}

	post, err := s.repository.Create(request.Context(), input)
	if err != nil {
		s.handleContentCreateAPIError(writer, request, err)
		return
	}
	s.writeContentAPISuccess(writer, http.StatusCreated, post, true)
}

func (s *Server) adminContentUpdateAPI(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAdminContentAPIEnabled(writer, request) {
		return
	}
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	current, ok := s.loadAdminContentAPI(writer, request)
	if !ok || !s.requireContentAPIObjectWrite(writer, request, principal, current) || !s.requireContentAPIPrecondition(writer, request, current) {
		return
	}
	var mutation adminContentMutationAPIRequest
	if _, ok := s.decodeStrictAPIJSON(writer, request, &mutation); !ok {
		return
	}
	if mutation.Empty() {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.empty_patch", "PATCH 至少需要一个可写字段", nil)
		return
	}
	input, fields := patchAPIInput(current, mutation, principal, time.Now())
	if len(fields) > 0 {
		s.writeContentValidationAPIError(writer, request, fields)
		return
	}
	updated, err := s.repository.Update(request.Context(), current.ID, content.Mutation{
		ExpectedVersion: current.Version, Actor: principal.Username,
	}, input)
	if err != nil {
		s.handleContentMutationAPIError(writer, request, current.ID, err)
		return
	}
	s.writeContentAPISuccess(writer, http.StatusOK, updated, false)
}

func (s *Server) adminContentDeleteAPI(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAdminContentAPIEnabled(writer, request) {
		return
	}
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok || !s.requireEmptyAPIBody(writer, request) {
		return
	}
	current, ok := s.loadAdminContentAPI(writer, request)
	if !ok || !s.requireContentAPIObjectWrite(writer, request, principal, current) || !s.requireContentAPIPrecondition(writer, request, current) {
		return
	}
	updated, err := s.repository.ChangeStatus(request.Context(), current.ID, content.Mutation{
		ExpectedVersion: current.Version, Actor: principal.Username,
	}, content.StatusTrash)
	if err != nil {
		s.handleContentMutationAPIError(writer, request, current.ID, err)
		return
	}
	s.writeContentAPISuccess(writer, http.StatusOK, updated, false)
}

func (s *Server) adminContentRevisionsAPI(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAdminContentAPIEnabled(writer, request) {
		return
	}
	principal, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityContentRead)
	if !ok {
		return
	}
	query, ok := s.strictAPIQuery(writer, request, map[string]bool{"limit": true})
	if !ok {
		return
	}
	limit, err := positiveQueryInt(query.Get("limit"), s.siteSettings.Snapshot().Settings.Reading.RevisionLimit, 100)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "limit 必须介于 1 和 100 之间", map[string]any{"field": "limit"})
		return
	}
	post, ok := s.loadAdminContentAPI(writer, request)
	if !ok {
		return
	}
	if !s.requireContentAPIObjectRead(writer, request, principal, post) {
		return
	}
	revisionsETag := contentRevisionsAPIETag(post)
	writer.Header().Set("ETag", revisionsETag)
	writer.Header().Set("Last-Modified", post.UpdatedAt.UTC().Format(http.TimeFormat))
	if etagMatches(request.Header.Get("If-None-Match"), revisionsETag) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	revisions, err := s.repository.ListRevisions(request.Context(), post.ID, limit)
	if errors.Is(err, content.ErrNotFound) {
		s.writeAPIError(writer, request, http.StatusNotFound, "content.not_found", "内容没有找到", nil)
		return
	}
	if err != nil {
		slog.Error("list content revisions API", "id", post.ID, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "content.revisions_unavailable", "修订记录暂时不可用", nil)
		return
	}
	items := make([]adminRevisionAPIResponse, 0, len(revisions))
	for _, revision := range revisions {
		items = append(items, adminRevisionAPIResponse{
			ID: revision.ID, PostID: revision.PostID, Version: revision.Version,
			Type: revision.Snapshot.Type, Status: revision.Snapshot.Status, Slug: revision.Snapshot.Slug,
			Title: revision.Snapshot.Title, Actor: revision.Actor, CreatedAt: revision.CreatedAt,
		})
	}
	writeJSON(writer, http.StatusOK, adminRevisionsAPIResponse{
		Items: items, Count: len(items), CurrentVersion: post.Version, CurrentETag: contentAPIETag(post),
	})
}

func (s *Server) adminContentRestoreRevisionAPI(writer http.ResponseWriter, request *http.Request) {
	if !s.requireAdminContentAPIEnabled(writer, request) {
		return
	}
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok || !s.requireEmptyAPIBody(writer, request) {
		return
	}
	post, ok := s.loadAdminContentAPI(writer, request)
	if !ok || !s.requireContentAPIObjectWrite(writer, request, principal, post) || !s.requireContentAPIPrecondition(writer, request, post) {
		return
	}
	revisionID, err := positivePathID(request.PathValue("revision_id"))
	if err != nil {
		s.writeAPIError(writer, request, http.StatusNotFound, "content.revision_not_found", "修订记录没有找到", nil)
		return
	}
	updated, err := s.repository.RestoreRevision(request.Context(), post.ID, revisionID, content.Mutation{
		ExpectedVersion: post.Version, Actor: principal.Username,
	})
	if err != nil {
		s.handleContentMutationAPIError(writer, request, post.ID, err)
		return
	}
	s.writeContentAPISuccess(writer, http.StatusOK, updated, false)
}
