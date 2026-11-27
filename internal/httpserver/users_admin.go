package httpserver

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/site"
)

type adminUsersView struct {
	OAuthProvider   string
	OAuthProviders  []loginProviderView
	Rows            []adminUserRow
	Roles           []auth.Role
	Form            adminUserForm
	Search          string
	Status          string
	Role            string
	Total           int
	ActiveCount     int
	SuspendedCount  int
	Page            int
	Pages           int
	PreviousURL     string
	NextURL         string
	HasPrevious     bool
	HasNext         bool
	CanManage       bool
	CanManageAssets bool
	OpenEditor      bool
}

type adminUserRow struct {
	OAuthLabel     string
	User           auth.User
	RoleLabel      string
	RoleSlugs      string
	StatusLabel    string
	StatusTone     string
	UpdatedLabel   string
	LastLoginLabel string
	EditURL        string
	IsCurrent      bool
}

type adminUserForm struct {
	OAuthProviders []loginProviderView
	ID             int64
	Version        int64
	Username       string
	Email          string
	DisplayName    string
	Status         string
	Locale         string
	RoleSlug       string
	IsCurrent      bool
	RoleSlugs      []string
	Bio            string
	AvatarMediaID  int64
	AvatarURL      string
	Phone          string
	EmailVerified  bool
	PhoneVerified  bool
	AccountDetails bool
	CreatedLabel   string
	LastLoginLabel string
	PasswordLogin  bool
}

func (form adminUserForm) HasRole(slug string) bool { return containsString(form.RoleSlugs, slug) }

func (s *Server) adminUsers(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/users/" || !s.auth.PersistentUsers() {
		s.renderError(writer, http.StatusNotFound, "用户与权限页面没有找到")
		return
	}
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityUsersRead)
	if !ok {
		return
	}
	queryValues, ok := strictQueryShape(request.URL.Query(), map[string]bool{
		"q": true, "status": true, "role": true, "page": true, "edit": true, "create": true, "notice": true, "oauth": true,
	})
	if !ok {
		s.renderError(writer, http.StatusBadRequest, "用户筛选参数不正确")
		return
	}
	page, err := positiveQueryInt(queryValues.Get("page"), 1, 100000)
	if err != nil {
		s.renderError(writer, http.StatusBadRequest, "分页参数不正确")
		return
	}
	search := strings.TrimSpace(queryValues.Get("q"))
	statusFilter := strings.TrimSpace(queryValues.Get("status"))
	roleFilter := strings.TrimSpace(queryValues.Get("role"))
	if _, err := auth.NormalizeUserQuery(auth.UserQuery{Search: search, Status: statusFilter, Role: roleFilter, OAuthProvider: queryValues.Get("oauth"), Limit: 1}); err != nil {
		s.renderError(writer, http.StatusBadRequest, "用户筛选参数不正确")
		return
	}
	form := defaultAdminUserForm()
	if rawID := strings.TrimSpace(queryValues.Get("edit")); rawID != "" {
		id, parseErr := strconv.ParseInt(rawID, 10, 64)
		if parseErr != nil || id <= 0 {
			s.renderError(writer, http.StatusBadRequest, "用户编号不正确")
			return
		}
		user, loadErr := s.auth.GetUser(request.Context(), id)
		if errors.Is(loadErr, auth.ErrUserNotFound) {
			s.renderError(writer, http.StatusNotFound, "用户没有找到")
			return
		}
		if loadErr != nil {
			slog.Error("load admin user", "id", id, "error", loadErr)
			s.renderError(writer, http.StatusInternalServerError, "用户信息暂时不可用")
			return
		}
		form = adminUserFormFromUser(user, principal.UserID)
	}
	s.renderAdminUsers(writer, request, http.StatusOK, principal, readOnly, search, statusFilter, roleFilter, page, form, nil, userNoticeFromQuery(queryValues.Get("notice")))
}

func (s *Server) adminUsersSubmit(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireUserManagementWrite(writer, request)
	if !ok {
		return
	}
	if form.Get("action") == "oauth-remove" {
		s.adminUserOAuthRemove(writer, request, principal, form)
		return
	}
	if invalidUserForm(form) != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_user_form_shape"})
		return
	}
	action := strings.TrimSpace(form.Get("action"))
	userForm, fields := parseAdminUserForm(form, principal.UserID)
	input := auth.UserInput{
		Username: userForm.Username, Email: userForm.Email, DisplayName: userForm.DisplayName,
		Status: userForm.Status, Locale: userForm.Locale, RoleSlugs: userForm.RoleSlugs,
	}
	if userForm.AccountDetails && action != "password" {
		input.Account = &auth.UserAccountInput{Bio: userForm.Bio, AvatarMediaID: userForm.AvatarMediaID, Phone: userForm.Phone, EmailVerified: userForm.EmailVerified, PhoneVerified: userForm.PhoneVerified}
	}
	input, validationFields := auth.NormalizeUserInput(input)
	for key, value := range validationFields {
		if key == "roles" {
			key = "role"
		}
		fields[key] = value
	}
	userForm.Username = input.Username
	userForm.Email = input.Email
	userForm.DisplayName = input.DisplayName
	userForm.Status = input.Status
	userForm.Locale = input.Locale
	if len(input.RoleSlugs) == 1 {
		userForm.RoleSlug = input.RoleSlugs[0]
	}
	userForm.RoleSlugs = input.RoleSlugs
	if input.Account != nil {
		userForm.Bio, userForm.Phone = input.Account.Bio, input.Account.Phone
	}

	if action == "password" || action == "create" {
		fields = passwordFormErrors(form, fields)
	} else if form.Get("password") != "" || form.Get("password_confirm") != "" {
		fields["password"] = "资料保存与密码轮换必须分别提交"
	}
	if userForm.IsCurrent && action == "update" && (input.Status != auth.UserStatusActive || !containsString(input.RoleSlugs, "administrator")) {
		fields["status"] = "不能停用自己的当前会话或移除自己的管理员角色"
		fields["role"] = "请先由另一位管理员调整此账号"
	}
	if len(fields) > 0 {
		s.renderAdminUsers(writer, request, http.StatusUnprocessableEntity, principal, false, "", "", "", 1, userForm, fields,
			adminNotice{Message: "请检查标记字段后再保存", Tone: "error"})
		return
	}

	var mutationErr error
	if input.Account != nil && (action == "update" || action == "create") {
		release, err := s.mediaUsage.HoldAvatar(input.Account.AvatarMediaID)
		if err != nil {
			s.renderAdminUsers(writer, request, http.StatusUnprocessableEntity, principal, false, "", "", "", 1, userForm,
				map[string]string{"avatar_media_id": "请重新选择媒体库中的可用图片，或移除头像后重试"}, adminNotice{Message: "头像当前不可用", Tone: "error"})
			return
		}
		defer release()
	}
	switch action {
	case "create":
		if userForm.ID != 0 || userForm.Version != 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unexpected_user_identity"})
			return
		}
		_, mutationErr = s.auth.CreateUser(request.Context(), principal, input, form.Get("password"))
	case "update":
		if userForm.ID <= 0 || userForm.Version <= 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_user_identity"})
			return
		}
		_, mutationErr = s.auth.UpdateUser(request.Context(), principal, userForm.ID, userForm.Version, input)
	case "password":
		if userForm.ID <= 0 || userForm.Version <= 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_user_identity"})
			return
		}
		current, loadErr := s.auth.GetUser(request.Context(), userForm.ID)
		if loadErr != nil {
			mutationErr = loadErr
			break
		}
		userForm = adminUserFormFromUser(current, principal.UserID)
		_, mutationErr = s.auth.UpdateUserPassword(request.Context(), principal, current.ID, formVersion(form), form.Get("password"))
		if mutationErr == nil && current.ID == principal.UserID {
			attemptKey := s.clientIPs.string(request)
			token, _, loginErr := s.auth.AuthenticateWithMetadataContext(
				request.Context(), attemptKey, current.Username, form.Get("password"),
				auth.SessionMetadata{IPAddress: attemptKey, UserAgent: request.UserAgent()},
			)
			if loginErr == nil {
				s.setAdminCookie(writer, token)
			} else if errors.Is(loginErr, auth.ErrSessionUnavailable) {
				s.sessionStorageError(writer, request)
				return
			} else {
				s.clearAdminCookie(writer)
			}
		}
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_user_action"})
		return
	}
	if mutationErr != nil {
		s.handleUserMutationError(writer, request, principal, action, userForm, mutationErr)
		return
	}
	notice := "updated"
	if action == "create" {
		notice = "created"
	} else if action == "password" {
		notice = "password"
	}
	redirect := "/admin/users/?notice=" + notice
	if userForm.ID > 0 {
		redirect += "&edit=" + strconv.FormatInt(userForm.ID, 10)
	}
	http.Redirect(writer, request, redirect, http.StatusSeeOther)
}

func (s *Server) renderAdminUsers(writer http.ResponseWriter, request *http.Request, statusCode int, principal auth.Principal, readOnly bool, search, statusFilter, roleFilter string, page int, form adminUserForm, fieldErrors map[string]string, notice adminNotice) {
	pageSize := max(1, s.siteSnapshot(request.Context()).Settings.Reading.AdminPageSize)
	oauthFilter := request.URL.Query().Get("oauth")
	query := auth.UserQuery{Search: search, Status: statusFilter, Role: roleFilter, OAuthProvider: oauthFilter, Limit: pageSize, Offset: (page - 1) * pageSize}
	result, err := s.auth.ListUsers(request.Context(), query)
	if err != nil {
		slog.Error("list admin users", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "用户列表暂时不可用")
		return
	}
	roles, err := s.auth.ListRoles(request.Context())
	if err != nil {
		slog.Error("list admin roles", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "角色目录暂时不可用")
		return
	}
	location := site.TimeLocation(s.siteSnapshot(request.Context()).Settings)
	rows := make([]adminUserRow, 0, len(result.Items))
	for _, user := range result.Items {
		lastLogin := "尚未登录"
		if !user.LastLoginAt.IsZero() {
			lastLogin = user.LastLoginAt.In(location).Format("2006-01-02 15:04")
		}
		statusLabel, statusTone := "已启用", "active"
		if user.Status == auth.UserStatusSuspended {
			statusLabel, statusTone = "已停用", "suspended"
		}
		roleSlugs := make([]string, 0, len(user.Roles))
		for _, role := range user.Roles {
			roleSlugs = append(roleSlugs, role.Slug)
		}
		rows = append(rows, adminUserRow{
			OAuthLabel: oauthBindingLabel(user),
			User:       user, RoleLabel: user.PrimaryRoleName(), RoleSlugs: strings.Join(roleSlugs, ", "),
			StatusLabel: statusLabel, StatusTone: statusTone, UpdatedLabel: user.UpdatedAt.In(location).Format("2006-01-02 15:04"),
			LastLoginLabel: lastLogin, EditURL: userAdminURL(search, statusFilter, roleFilter, page, user.ID), IsCurrent: user.ID == principal.UserID,
		})
	}
	pages := max(1, (result.Total+pageSize-1)/pageSize)
	data := s.baseViewFor(writer, "用户与权限", "管理账号、角色、权限范围与账号生命周期", "/admin/users/")
	data.BodyClass = "is-admin is-admin-users"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("users", principal, readOnly)
	data.Admin.FieldErrors = fieldErrors
	data.Admin.Notice = notice.Message
	data.Admin.NoticeTone = notice.Tone
	data.Users = adminUsersView{
		Rows: rows, Roles: roles, Form: form, Search: search, Status: statusFilter, Role: roleFilter,
		Total: result.Total, ActiveCount: result.StatusCounts[auth.UserStatusActive], SuspendedCount: result.StatusCounts[auth.UserStatusSuspended],
		Page: page, Pages: pages, HasPrevious: page > 1, HasNext: page < pages,
		PreviousURL:     userAdminURL(search, statusFilter, roleFilter, max(1, page-1), 0),
		NextURL:         userAdminURL(search, statusFilter, roleFilter, min(pages, page+1), 0),
		CanManage:       !readOnly && principal.Can(auth.CapabilityUsersManage),
		CanManageAssets: !readOnly && principal.Can(auth.CapabilityUsersManage) && principal.Can(auth.CapabilitySettingsManage),
		OpenEditor:      form.ID > 0 || len(fieldErrors) > 0 || request.URL.Query().Get("create") == "1",
	}
	data.Users.Form.AvatarURL = s.avatarURL(form.AvatarMediaID)
	data.Users.OAuthProvider = oauthFilter
	for _, id := range site.LoginProviderOrder {
		data.Users.OAuthProviders = append(data.Users.OAuthProviders, loginProviderView{ID: id, Name: site.LoginProviderNames[id]})
	}
	withFilter := func(raw string) string {
		u, _ := url.Parse(raw)
		q := u.Query()
		if oauthFilter != "" {
			q.Set("oauth", oauthFilter)
		}
		u.RawQuery = q.Encode()
		return u.String()
	}
	data.Users.PreviousURL = withFilter(data.Users.PreviousURL)
	data.Users.NextURL = withFilter(data.Users.NextURL)
	for i := range data.Users.Rows {
		data.Users.Rows[i].EditURL = withFilter(data.Users.Rows[i].EditURL)
	}
	if form.ID > 0 {
		if user, err := s.auth.GetUser(request.Context(), form.ID); err == nil {
			for _, id := range site.LoginProviderOrder {
				if user.OAuthBindings[id] != "" {
					data.Users.Form.OAuthProviders = append(data.Users.Form.OAuthProviders, loginProviderView{ID: id, Name: site.LoginProviderNames[id]})
				}
			}
			data.Users.Form.CreatedLabel = user.CreatedAt.In(location).Format("2006-01-02 15:04")
			data.Users.Form.LastLoginLabel = "尚未登录"
			if !user.LastLoginAt.IsZero() {
				data.Users.Form.LastLoginLabel = user.LastLoginAt.In(location).Format("2006-01-02 15:04")
			}
		}
	}
	s.render(writer, statusCode, "admin-users", data)
}

func (s *Server) requireUserManagementWrite(writer http.ResponseWriter, request *http.Request) (auth.Principal, url.Values, bool) {
	if !s.auth.PersistentUsers() {
		http.NotFound(writer, request)
		return auth.Principal{}, nil, false
	}
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityUsersManage)
	if !ok {
		return auth.Principal{}, nil, false
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return auth.Principal{}, nil, false
	}
	if !s.sameOriginRequest(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return auth.Principal{}, nil, false
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") || len(parameters) != 0 {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "form_content_type_required"})
		return auth.Principal{}, nil, false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
	if err := request.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]string{"error": "user_form_too_large"})
			return auth.Principal{}, nil, false
		}
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_user_form"})
		return auth.Principal{}, nil, false
	}
	form := request.PostForm
	if len(form["csrf_token"]) != 1 || !s.auth.CheckCSRF(principal, form.Get("csrf_token")) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return auth.Principal{}, nil, false
	}
	return principal, form, true
}

func parseAdminUserForm(form url.Values, currentUserID int64) (adminUserForm, map[string]string) {
	result := adminUserForm{
		Username: form.Get("username"), Email: form.Get("email"), DisplayName: form.Get("display_name"),
		Status: form.Get("status"), Locale: form.Get("locale"), RoleSlug: form.Get("role"),
		RoleSlugs: append([]string(nil), form["role"]...),
		Bio:       form.Get("bio"), Phone: form.Get("phone"),
		EmailVerified: form.Get("email_verified") == "true", PhoneVerified: form.Get("phone_verified") == "true",
	}
	fields := make(map[string]string)
	for _, key := range []string{"account_details", "bio", "phone", "avatar_media_id", "email_verified", "phone_verified"} {
		if _, exists := form[key]; exists {
			result.AccountDetails = true
		}
	}
	if result.AccountDetails {
		for _, key := range []string{"bio", "phone", "avatar_media_id", "email_verified", "phone_verified"} {
			if len(form[key]) != 1 {
				fields[key] = "请提交完整的账户资料"
			}
		}
		for _, key := range []string{"email_verified", "phone_verified"} {
			if form.Get(key) != "true" && form.Get(key) != "false" {
				fields[key] = "验证状态不正确"
			}
		}
		id, err := strconv.ParseInt(strings.TrimSpace(form.Get("avatar_media_id")), 10, 64)
		if err != nil || id < 0 {
			fields["avatar_media_id"] = "头像编号需为非负整数"
		} else {
			result.AvatarMediaID = id
		}
	}
	if raw := strings.TrimSpace(form.Get("id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			fields["id"] = "用户编号不正确"
		} else {
			result.ID = id
			result.IsCurrent = id == currentUserID
		}
	}
	if raw := strings.TrimSpace(form.Get("version")); raw != "" {
		version, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || version <= 0 {
			fields["version"] = "用户版本不正确"
		} else {
			result.Version = version
		}
	}
	return result, fields
}

func passwordFormErrors(form url.Values, fields map[string]string) map[string]string {
	password := form.Get("password")
	if password == "" {
		fields["password"] = "请输入新密码"
	} else if password != form.Get("password_confirm") {
		fields["password_confirm"] = "两次输入的密码不一致"
	}
	return fields
}

func invalidUserForm(form url.Values) string {
	allowed := map[string]bool{
		"csrf_token": true, "action": true, "id": true, "version": true,
		"username": true, "email": true, "display_name": true, "status": true,
		"locale": true, "role": true, "password": true, "password_confirm": true,
		"account_details": true, "bio": true, "avatar_media_id": true, "phone": true, "email_verified": true, "phone_verified": true,
	}
	for key, values := range form {
		if key == "role" && len(values) > 0 && len(values) <= 32 {
			continue
		}
		if !allowed[key] || len(values) != 1 {
			return key
		}
	}
	return ""
}

func (s *Server) handleUserMutationError(writer http.ResponseWriter, request *http.Request, principal auth.Principal, action string, form adminUserForm, err error) {
	statusCode := http.StatusUnprocessableEntity
	fields := auth.UserFieldErrors(err)
	notice := adminNotice{Message: "用户资料没有通过服务端校验", Tone: "error"}
	switch {
	case errors.Is(err, auth.ErrSessionUnavailable):
		s.sessionStorageError(writer, request)
		return
	case errors.Is(err, auth.ErrRateLimited):
		writer.Header().Set("Retry-After", "1")
		s.renderError(writer, http.StatusTooManyRequests, "密码操作较多，请稍后重试")
		return
	case errors.Is(err, auth.ErrUsernameConflict):
		statusCode = http.StatusConflict
		fields = map[string]string{"username": "这个用户名已被其他账号使用"}
		notice.Message = "用户名冲突，请更换后重试"
	case errors.Is(err, auth.ErrEmailConflict):
		statusCode = http.StatusConflict
		fields = map[string]string{"email": "这个邮箱已被其他账号使用"}
		notice.Message = "邮箱冲突，请更换后重试"
	case errors.Is(err, auth.ErrPhoneConflict):
		statusCode = http.StatusConflict
		fields = map[string]string{"phone": "这个手机号已被其他账号使用"}
		notice.Message = "手机号冲突，请更换后重试"
	case errors.Is(err, auth.ErrUserVersionConflict):
		statusCode = http.StatusConflict
		fields = map[string]string{"version": "账号刚刚被其他请求修改；已保留你的输入，请确认后重新提交"}
		notice.Message = "版本冲突：请复核当前账号状态"
	case errors.Is(err, auth.ErrLastAdministrator):
		statusCode = http.StatusConflict
		fields = map[string]string{"role": "至少必须保留一位已启用的管理员", "status": "最后一位管理员不能停用"}
		notice.Message = "操作会让站点失去可用管理员"
	case errors.Is(err, auth.ErrRoleNotFound):
		fields = map[string]string{"role": "选择的角色已经不存在"}
	case errors.Is(err, auth.ErrUserNotFound):
		s.renderError(writer, http.StatusNotFound, "用户没有找到")
		return
	case len(fields) == 0:
		slog.Error("mutate admin user", "action", action, "id", form.ID, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "用户资料暂时无法保存")
		return
	}
	if form.ID > 0 {
		if current, loadErr := s.auth.GetUser(request.Context(), form.ID); loadErr == nil {
			form.Version = current.Version
		}
	}
	s.renderAdminUsers(writer, request, statusCode, principal, false, "", "", "", 1, form, fields, notice)
}

func defaultAdminUserForm() adminUserForm {
	return adminUserForm{Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlug: "author", RoleSlugs: []string{"author"}, AccountDetails: true}
}

func adminUserFormFromUser(user auth.User, currentUserID int64) adminUserForm {
	role := ""
	if len(user.Roles) > 0 {
		role = user.Roles[0].Slug
		for _, candidate := range []string{"administrator", "editor", "author", "subscriber"} {
			if user.HasRole(candidate) {
				role = candidate
				break
			}
		}
	}
	roles := make([]string, 0, len(user.Roles))
	for _, role := range user.Roles {
		roles = append(roles, role.Slug)
	}
	return adminUserForm{
		ID: user.ID, Version: user.Version, Username: user.Username, Email: user.Email,
		DisplayName: user.DisplayName, Status: user.Status, Locale: user.Locale,
		RoleSlug: role, IsCurrent: user.ID == currentUserID,
		RoleSlugs: roles, Bio: user.Bio, AvatarMediaID: user.AvatarMediaID, Phone: user.Phone,
		EmailVerified: user.EmailVerifiedAt != nil, PhoneVerified: user.PhoneVerifiedAt != nil, AccountDetails: true, PasswordLogin: user.PasswordLogin,
	}
}

func userAdminURL(search, statusFilter, roleFilter string, page int, editID int64) string {
	values := make(url.Values)
	if search != "" {
		values.Set("q", search)
	}
	if statusFilter != "" {
		values.Set("status", statusFilter)
	}
	if roleFilter != "" {
		values.Set("role", roleFilter)
	}
	if page > 1 {
		values.Set("page", strconv.Itoa(page))
	}
	if editID > 0 {
		values.Set("edit", strconv.FormatInt(editID, 10))
	}
	if encoded := values.Encode(); encoded != "" {
		return "/admin/users/?" + encoded
	}
	return "/admin/users/"
}

func userNoticeFromQuery(value string) adminNotice {
	switch value {
	case "created":
		return adminNotice{Message: "用户已创建，可以立即使用用户名或邮箱登录", Tone: "success"}
	case "updated":
		return adminNotice{Message: "账号资料、状态与角色能力已同步更新", Tone: "success"}
	case "password":
		return adminNotice{Message: "密码已通过 Argon2id 重新生成，旧会话全部撤销", Tone: "success"}
	default:
		return adminNotice{}
	}
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func formVersion(form url.Values) int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(form.Get("version")), 10, 64)
	return value
}
