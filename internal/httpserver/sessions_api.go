package httpserver

import (
	"errors"
	"net/http"
	"time"

	"newpress.local/newpress/internal/auth"
)

type adminSessionsAPIResponse struct {
	Items        []adminSessionAPIItem `json:"items"`
	Scope        string                `json:"scope"`
	Count        int                   `json:"count"`
	MineCount    int                   `json:"mine_count"`
	UserCount    int                   `json:"user_count"`
	ExpiringSoon int                   `json:"expiring_soon"`
	ReadOnly     bool                  `json:"read_only"`
	GeneratedAt  time.Time             `json:"generated_at"`
}

type adminSessionAPIItem struct {
	ID            string    `json:"id"`
	UserID        int64     `json:"user_id,omitempty"`
	Username      string    `json:"username"`
	DisplayName   string    `json:"display_name"`
	Role          string    `json:"role"`
	IPAddress     string    `json:"ip_address,omitempty"`
	UserAgent     string    `json:"user_agent,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	IdleExpiresAt time.Time `json:"idle_expires_at"`
	Current       bool      `json:"current"`
}

type adminSessionsRevokeOthersAPIResponse struct {
	Revoked int `json:"revoked"`
}

func (s *Server) adminSessionsAPI(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilitySessionsRead)
	if !ok {
		return
	}
	query, ok := s.strictAPIQuery(writer, request, map[string]bool{"scope": true})
	if !ok {
		return
	}
	scope, err := resolveAdminSessionScope(query.Get("scope"), principal, readOnly)
	if errors.Is(err, errSessionScopeForbidden) {
		s.writeAPIError(writer, request, http.StatusForbidden, "sessions.scope_forbidden", "当前账号只能查看自己的会话", map[string]any{"scope": "mine"})
		return
	}
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_query", "scope 必须是 mine 或 all", map[string]any{"field": "scope"})
		return
	}
	currentToken := ""
	if !readOnly {
		_, currentToken, _ = s.currentAdminSession(request)
	}
	mine, err := s.auth.ListSessionsContext(request.Context(), principal, currentToken, false)
	if err != nil {
		s.sessionStorageError(writer, request)
		return
	}
	selected := mine
	if scope == "all" && principal.Can(auth.CapabilitySessionsManage) {
		selected, err = s.auth.ListSessionsContext(request.Context(), principal, currentToken, true)
		if err != nil {
			s.sessionStorageError(writer, request)
			return
		}
	}
	now := time.Now()
	identities := make(map[string]struct{})
	expiringSoon := 0
	items := make([]adminSessionAPIItem, 0, len(selected))
	for _, item := range selected {
		identities[sessionIdentityKey(item)] = struct{}{}
		if remaining := item.IdleExpiresAt.Sub(now); remaining > 0 && remaining <= 15*time.Minute {
			expiringSoon++
		}
		items = append(items, adminSessionAPIItemFromAuth(item))
	}
	writeJSON(writer, http.StatusOK, adminSessionsAPIResponse{
		Items: items, Scope: scope, Count: len(items), MineCount: len(mine), UserCount: len(identities),
		ExpiringSoon: expiringSoon, ReadOnly: readOnly, GeneratedAt: now.UTC(),
	})
}

func (s *Server) adminSessionDeleteAPI(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilitySessionsManage, auth.CapabilitySessionsOwn)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	if !s.requireEmptyAPIBody(writer, request) {
		return
	}
	token, ok := s.currentAdminTokenAPI(writer, request)
	if !ok {
		return
	}
	if _, err := s.auth.RevokeSessionIDContext(request.Context(), principal, token, request.PathValue("session_id")); err != nil {
		s.writeSessionAPIError(writer, request, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminSessionsRevokeOthersAPI(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilitySessionsManage, auth.CapabilitySessionsOwn)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	if !s.requireEmptyAPIBody(writer, request) {
		return
	}
	token, ok := s.currentAdminTokenAPI(writer, request)
	if !ok {
		return
	}
	revoked, err := s.auth.RevokeOtherSessionsContext(request.Context(), principal, token)
	if err != nil {
		s.writeSessionAPIError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, adminSessionsRevokeOthersAPIResponse{Revoked: revoked})
}

func (s *Server) currentAdminTokenAPI(writer http.ResponseWriter, request *http.Request) (string, bool) {
	_, token, ok := s.currentAdminSession(request)
	if ok && token != "" {
		return token, true
	}
	writer.Header().Set("WWW-Authenticate", `NewPressSession realm="admin"`)
	s.writeAPIError(writer, request, http.StatusUnauthorized, "auth.authentication_required", "需要有效的管理员会话", nil)
	return "", false
}

func (s *Server) writeSessionAPIError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrSessionUnavailable):
		s.sessionStorageError(writer, request)
	case errors.Is(err, auth.ErrSessionNotFound):
		s.writeAPIError(writer, request, http.StatusNotFound, "sessions.not_found", "会话没有找到或已经失效", nil)
	case errors.Is(err, auth.ErrSessionForbidden):
		s.writeAPIError(writer, request, http.StatusForbidden, "sessions.owner_required", "当前账号不能撤销其他身份的会话", nil)
	case errors.Is(err, auth.ErrCurrentSession):
		s.writeAPIError(writer, request, http.StatusConflict, "sessions.current_session", "当前会话必须通过退出登录结束", map[string]any{"logout": "/admin/logout/"})
	default:
		s.writeAPIError(writer, request, http.StatusInternalServerError, "sessions.operation_failed", "会话操作暂时无法完成", nil)
	}
}

func adminSessionAPIItemFromAuth(item auth.SessionInfo) adminSessionAPIItem {
	return adminSessionAPIItem{
		ID: item.ID, UserID: item.UserID, Username: item.Username, DisplayName: item.DisplayName, Role: item.RoleName,
		IPAddress: item.IPAddress, UserAgent: item.UserAgent, CreatedAt: item.CreatedAt.UTC(), LastSeenAt: item.LastSeenAt.UTC(),
		ExpiresAt: item.ExpiresAt.UTC(), IdleExpiresAt: item.IdleExpiresAt.UTC(), Current: item.Current,
	}
}
