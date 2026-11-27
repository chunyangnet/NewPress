package httpserver

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

func oauthBindingLabel(u auth.User) string {
	names := []string{}
	for _, id := range site.LoginProviderOrder {
		if u.OAuthBindings[id] != "" {
			names = append(names, site.LoginProviderNames[id])
		}
	}
	if len(names) == 0 {
		return "未绑定第三方平台"
	}
	return strings.Join(names, " · ")
}

func (s *Server) adminUserOAuthRemove(w http.ResponseWriter, r *http.Request, actor auth.Principal, form url.Values) {
	allowed := map[string]bool{"csrf_token": true, "action": true, "id": true, "version": true, "provider": true}
	for key, values := range form {
		if !allowed[key] || len(values) != 1 {
			s.renderError(w, 400, "解绑参数不正确")
			return
		}
	}
	id, err := strconv.ParseInt(form.Get("id"), 10, 64)
	version, versionErr := strconv.ParseInt(form.Get("version"), 10, 64)
	if err != nil || versionErr != nil || id <= 0 || version <= 0 || site.LoginProviderNames[form.Get("provider")] == "" {
		s.renderError(w, 400, "解绑参数不正确")
		return
	}
	u, err := s.auth.GetUser(r.Context(), id)
	if err != nil {
		s.profileError(w, r, err)
		return
	}
	u.Version = version
	_, err = s.auth.AdminRemoveOAuthBinding(r.Context(), actor, u, form.Get("provider"))
	if err != nil {
		if errors.Is(err, auth.ErrSessionUnavailable) {
			s.sessionStorageError(w, r)
			return
		}
		message := "解绑失败，请刷新账号信息后重试"
		if errors.Is(err, auth.ErrLastLoginMethod) {
			message = "不能移除最后一种登录方式，请先为账号设置密码"
		}
		s.renderAdminUsers(w, r, http.StatusConflict, actor, false, "", "", "", 1, adminUserFormFromUser(u, actor.UserID), nil, adminNotice{Message: message, Tone: "error"})
		return
	}
	http.Redirect(w, r, "/admin/users/?notice=updated&edit="+strconv.FormatInt(id, 10), http.StatusSeeOther)
}
