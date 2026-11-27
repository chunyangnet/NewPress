package httpserver

import (
	"net/http"
	"newpress.local/newpress/internal/site"
)

func (s *Server) socialOAuthBindings(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	u, err := s.auth.GetUser(r.Context(), p.UserID)
	if err != nil {
		s.profileError(w, r, err)
		return
	}
	items := []map[string]any{}
	for _, id := range site.LoginProviderOrder {
		_, enabled := s.loginProvider(id)
		bound := u.OAuthBindings[id] != ""
		if !enabled && !bound {
			continue
		}
		items = append(items, map[string]any{"id": id, "name": site.LoginProviderNames[id], "bound": bound, "enabled": enabled && s.oauthRedirect(id) != ""})
	}
	writeJSON(w, 200, map[string]any{"items": items, "version": u.Version})
}

func (s *Server) socialOAuthRemove(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version int64 `json:"version"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 1024, "1 KiB"); !ok {
		return
	}
	u, err := s.auth.GetUser(r.Context(), p.UserID)
	if err != nil {
		s.profileError(w, r, err)
		return
	}
	u.Version = input.Version
	if _, err = s.auth.UpdateOAuthBinding(r.Context(), u, r.PathValue("provider"), ""); err != nil {
		s.writeAPIError(w, r, 409, "auth.binding_conflict", "解绑失败：请保留至少一种登录方式，并刷新后重试", nil)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
