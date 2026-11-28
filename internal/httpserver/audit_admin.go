package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
)

const (
	adminAuditViewLimit     = 8
	adminAuditExportLimit   = 100
	adminAuditReadTimeout   = 2 * time.Second
	auditExportDocumentKind = "newpress.audit_export"
)

type adminAuditView struct {
	Available        bool
	Events           []adminAuditEventView
	Total            int
	HasMore          bool
	FilterAction     string
	FilterObjectType string
	FilterActive     bool
	NextURL          string
	LatestURL        string
	ExportURL        string
	APIURL           string
}

type adminAuditEventView struct {
	CreatedAt   time.Time
	Action      string
	ActionLabel string
	ObjectType  string
	ObjectLabel string
	ObjectID    string
	Actor       string
	ActorLabel  string
	Tone        string
}

type auditEventAPIResponse struct {
	CreatedAt  time.Time `json:"created_at"`
	Action     string    `json:"action"`
	ObjectType string    `json:"object_type"`
	ObjectID   string    `json:"object_id,omitempty"`
	Actor      string    `json:"actor"`
}

type auditEventsAPIResponse struct {
	Items      []auditEventAPIResponse    `json:"items"`
	Pagination auditPaginationAPIResponse `json:"pagination"`
}

type auditPaginationAPIResponse struct {
	PerPage    int    `json:"per_page"`
	Returned   int    `json:"returned"`
	Total      int    `json:"total"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
	Next       string `json:"next,omitempty"`
}

type auditExportDocument struct {
	Schema     int                     `json:"schema"`
	Kind       string                  `json:"kind"`
	ExportedAt time.Time               `json:"exported_at"`
	Filters    auditExportFilters      `json:"filters"`
	Events     []auditEventAPIResponse `json:"events"`
	Pagination auditExportPagination   `json:"pagination"`
}

type auditExportFilters struct {
	Action     string `json:"action,omitempty"`
	ObjectType string `json:"object_type,omitempty"`
}

type auditExportPagination struct {
	Limit      int    `json:"limit"`
	Returned   int    `json:"returned"`
	Total      int    `json:"total"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func (s *Server) adminAuditEventsAPI(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilitySystemRead); !ok {
		return
	}
	values, ok := s.strictAPIQuery(writer, request, map[string]bool{
		"action": true, "object_type": true, "cursor": true, "per_page": true,
	})
	if !ok {
		return
	}
	perPage, err := positiveQueryInt(values.Get("per_page"), audit.DefaultReadLimit, audit.MaxReadLimit)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "per_page 必须介于 1 和 100 之间", map[string]any{"field": "per_page"})
		return
	}
	query, err := audit.NormalizeQuery(audit.Query{
		Limit: perPage, Cursor: values.Get("cursor"), Action: values.Get("action"), ObjectType: values.Get("object_type"),
	})
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "audit.invalid_query", "审计筛选或游标不正确", nil)
		return
	}
	page, err := s.auditPage(request.Context(), query)
	access := func() error { return s.adminReadAccess(request, auth.CapabilitySystemRead) }
	if !s.recheckDiagnosticPage(writer, request, access) {
		return
	}
	if err != nil {
		slog.Error("list audit events API", "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusServiceUnavailable, "audit.unavailable", "审计事件暂时不可用", nil)
		return
	}
	response := auditEventsAPIResponse{Items: auditEventResponses(page.Events)}
	response.Pagination = auditPaginationAPIResponse{
		PerPage: perPage, Returned: len(response.Items), Total: page.Total, HasMore: page.HasMore, NextCursor: page.NextCursor,
	}
	if page.HasMore {
		next := query
		next.Cursor = page.NextCursor
		response.Pagination.Next = auditAPIURL(next)
	}
	writePrivateJSON(writer, request, http.StatusOK, response, nil, access, func(err error) { s.diagnosticReadError(writer, request, err) })
}

func (s *Server) adminToolsAuditExport(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminPageCapability(writer, request, auth.CapabilitySystemRead); !ok {
		return
	}
	values, ok := s.strictAPIQuery(writer, request, map[string]bool{
		"action": true, "object_type": true, "cursor": true, "limit": true,
	})
	if !ok {
		return
	}
	limit, err := positiveQueryInt(values.Get("limit"), adminAuditExportLimit, audit.MaxReadLimit)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "limit 必须介于 1 和 100 之间", map[string]any{"field": "limit"})
		return
	}
	query, err := audit.NormalizeQuery(audit.Query{
		Limit: limit, Cursor: values.Get("cursor"), Action: values.Get("action"), ObjectType: values.Get("object_type"),
	})
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "audit.invalid_query", "审计筛选或游标不正确", nil)
		return
	}
	page, err := s.auditPage(request.Context(), query)
	if !s.recheckDiagnosticPage(writer, request, func() error { return s.adminReadAccess(request, auth.CapabilitySystemRead) }) {
		return
	}
	if err != nil {
		slog.Error("export audit events", "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusServiceUnavailable, "audit.unavailable", "审计事件暂时不可导出", nil)
		return
	}
	document := auditExportDocument{
		Schema: 1, Kind: auditExportDocumentKind, ExportedAt: time.Now().UTC(),
		Filters: auditExportFilters{Action: query.Action, ObjectType: query.ObjectType},
		Events:  auditEventResponses(page.Events),
		Pagination: auditExportPagination{
			Limit: limit, Returned: min(limit, len(page.Events)), Total: page.Total,
			HasMore: page.HasMore, NextCursor: page.NextCursor,
		},
	}
	if err := s.writeAdminToolJSONAttachment(writer, request, auth.CapabilitySystemRead, "newpress-audit-events.json", document); err != nil {
		slog.Error("write audit export", "error", err)
	}
}

func (s *Server) auditPage(parent context.Context, query audit.Query) (audit.Page, error) {
	if s.auditEvents == nil {
		return audit.Page{}, errors.New("audit reader is not configured")
	}
	ctx, cancel := context.WithTimeout(parent, adminAuditReadTimeout)
	defer cancel()
	page, err := s.auditEvents.List(ctx, query)
	if contextErr := ctx.Err(); contextErr != nil {
		return audit.Page{}, contextErr
	}
	return page, err
}

func (s *Server) auditToolsView(ctx context.Context, query audit.Query) adminAuditView {
	view := adminAuditView{
		FilterAction: query.Action, FilterObjectType: query.ObjectType,
		FilterActive: query.Action != "" || query.ObjectType != "" || query.Cursor != "",
		LatestURL:    adminAuditPageURL(query, ""), ExportURL: auditExportURL(query), APIURL: auditAPIURL(query),
	}
	page, err := s.auditPage(ctx, query)
	if err != nil {
		slog.Warn("load audit tools view", "error", err)
		return view
	}
	view.Available = true
	view.Total = page.Total
	view.HasMore = page.HasMore
	view.Events = make([]adminAuditEventView, 0, len(page.Events))
	for _, event := range page.Events {
		view.Events = append(view.Events, adminAuditEventProjection(event))
	}
	if page.HasMore {
		view.NextURL = adminAuditPageURL(query, page.NextCursor)
	}
	return view
}

func auditToolsQuery(values url.Values) (audit.Query, error) {
	return audit.NormalizeQuery(audit.Query{
		Limit: adminAuditViewLimit, Cursor: values.Get("audit_cursor"),
		Action: values.Get("audit_action"), ObjectType: values.Get("audit_object_type"),
	})
}

func adminAuditEventProjection(event audit.Entry) adminAuditEventView {
	return adminAuditEventView{
		CreatedAt: event.CreatedAt, Action: event.Action, ActionLabel: auditActionLabel(event.Action),
		ObjectType: event.ObjectType, ObjectLabel: auditObjectLabel(event.ObjectType), ObjectID: event.ObjectID,
		Actor: event.Actor, ActorLabel: auditActorLabel(event.Actor), Tone: auditObjectTone(event.ObjectType),
	}
}

func auditEventResponses(events []audit.Entry) []auditEventAPIResponse {
	items := make([]auditEventAPIResponse, 0, len(events))
	for _, event := range events {
		items = append(items, auditEventAPIResponse{
			CreatedAt: event.CreatedAt.UTC(), Action: event.Action, ObjectType: event.ObjectType,
			ObjectID: event.ObjectID, Actor: event.Actor,
		})
	}
	return items
}

func auditActionLabel(action string) string {
	if strings.HasPrefix(action, "community.") {
		if action == "community.delivery.ack" {
			return "确认圈子通知入队"
		}
		return circleActionLabel(strings.TrimPrefix(action, "community."))
	}
	labels := map[string]string{
		"plugin.content.updated":      "插件内容操作提交",
		"audit.fact.baseline":         "建立事实基线",
		"user.roles.ensure":           "同步内置角色",
		"user.created":                "创建用户",
		"user.updated":                "更新用户",
		"user.password_changed":       "轮换用户密码",
		"user.login_recorded":         "记录最近登录",
		"comment.seeded":              "写入评论种子",
		"comment.created":             "创建评论",
		"comment.edit":                "编辑评论",
		"comment.revision.restore":    "恢复评论修订",
		"comment.reply":               "添加站方回复",
		"comment.delete":              "删除评论树",
		"navigation.seeded":           "写入导航种子",
		"navigation.menu.create":      "创建导航菜单",
		"navigation.menu.update":      "更新导航菜单",
		"navigation.items.replace":    "替换菜单项目",
		"navigation.menu.delete":      "删除导航菜单",
		"navigation.registry.replace": "恢复导航注册表",
	}
	if label := labels[action]; label != "" {
		return label
	}
	if strings.HasPrefix(action, "comment.status.") {
		return "更新评论状态"
	}
	return "记录持久变更"
}

func auditObjectLabel(objectType string) string {
	if strings.HasPrefix(objectType, "community_") {
		return "圈子"
	}
	switch objectType {
	case "user":
		return "用户"
	case "role_registry":
		return "角色注册表"
	case "comment":
		return "评论"
	case "comment_batch":
		return "评论批次"
	case "comment_registry":
		return "评论注册表"
	case "navigation_menu":
		return "导航菜单"
	case "navigation_registry":
		return "导航注册表"
	case "storage_fact":
		return "存储事实"
	default:
		return objectType
	}
}

func auditObjectTone(objectType string) string {
	switch {
	case objectType == "user" || objectType == "role_registry":
		return "identity"
	case strings.HasPrefix(objectType, "comment"):
		return "comment"
	case strings.HasPrefix(objectType, "navigation"):
		return "navigation"
	default:
		return "system"
	}
}

func auditActorLabel(actor string) string {
	switch actor {
	case "system":
		return "系统"
	case "migration":
		return "迁移进程"
	case "operator":
		return "已认证操作员"
	default:
		return "未知主体"
	}
}

func adminAuditPageURL(query audit.Query, cursor string) string {
	values := url.Values{}
	if query.Action != "" {
		values.Set("audit_action", query.Action)
	}
	if query.ObjectType != "" {
		values.Set("audit_object_type", query.ObjectType)
	}
	if cursor != "" {
		values.Set("audit_cursor", cursor)
	}
	path := "/admin/tools/"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return path + "#audit-events"
}

func auditExportURL(query audit.Query) string {
	values := auditQueryValues(query, "limit", adminAuditExportLimit)
	path := "/admin/tools/audit-events.json"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return path
}

func auditAPIURL(query audit.Query) string {
	values := auditQueryValues(query, "per_page", query.Limit)
	path := "/api/v1/admin/audit-events"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return path
}

func auditQueryValues(query audit.Query, limitKey string, limit int) url.Values {
	values := url.Values{}
	if query.Action != "" {
		values.Set("action", query.Action)
	}
	if query.ObjectType != "" {
		values.Set("object_type", query.ObjectType)
	}
	if query.Cursor != "" {
		values.Set("cursor", query.Cursor)
	}
	if limit > 0 {
		values.Set(limitKey, strconv.Itoa(limit))
	}
	return values
}
