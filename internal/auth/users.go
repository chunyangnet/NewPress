package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	UserStatusActive    = "active"
	UserStatusSuspended = "suspended"
	UserStatusSystem    = "system"

	CapabilityAll              = "*"
	CapabilityAdminAccess      = "admin:access"
	CapabilityDashboardRead    = "dashboard:read"
	CapabilityContentRead      = "content:read"
	CapabilityContentWrite     = "content:write"
	CapabilityContentWriteOwn  = "content:write:own"
	CapabilityTaxonomyManage   = "taxonomy:manage"
	CapabilityMediaRead        = "media:read"
	CapabilityMediaWrite       = "media:write"
	CapabilityCommentsModerate = "comments:moderate"
	CapabilityUsersRead        = "users:read"
	CapabilityUsersManage      = "users:manage"
	CapabilitySessionsRead     = "sessions:read"
	CapabilitySessionsManage   = "sessions:manage"
	CapabilitySessionsOwn      = "sessions:manage:own"
	CapabilityAppearanceManage = "appearance:manage"
	CapabilityNavigationManage = "navigation:manage"
	CapabilitySettingsManage   = "settings:manage"
	CapabilityPluginsManage    = "plugins:manage"
	CapabilitySystemRead       = "system:read"
	CapabilitySystemUpdate     = "system:update"
	CapabilityProfileWriteOwn  = "profile:write:own"

	maxUserSearchRunes = 100
)

var (
	ErrUserNotFound          = errors.New("user not found")
	ErrUserVersionConflict   = errors.New("user version conflict")
	ErrUsernameConflict      = errors.New("username already exists")
	ErrEmailConflict         = errors.New("email already exists")
	ErrRoleNotFound          = errors.New("role not found")
	ErrLastAdministrator     = errors.New("last active administrator cannot be removed")
	ErrUserStoreNotAvailable = errors.New("persistent user store is not available")
	ErrFirstAdminSetupClosed = errors.New("first administrator setup is closed")

	localePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$`)
)

// Role is a named collection of capabilities. Built-in roles are seeded into
// every persistent store, while the capability list remains the authorization
// source of truth.
type Role struct {
	ID           int64    `json:"id,omitempty"`
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities"`
	BuiltIn      bool     `json:"built_in"`
	UserCount    int      `json:"user_count,omitempty"`
}

// User is the canonical persistent administrator/editor identity. PasswordHash
// is intentionally never projected by HTTP response DTOs or templates.
type User struct {
	Referral            ReferralData      `json:"-"`
	ID                  int64             `json:"id"`
	Username            string            `json:"username"`
	Email               string            `json:"email"`
	Phone               string            `json:"phone,omitempty"`
	PhoneVerifiedAt     *time.Time        `json:"phone_verified_at,omitempty"`
	OAuthBindings       map[string]string `json:"-"`
	RegistrationKey     string            `json:"-"`
	RegistrationVersion int64             `json:"-"`
	RegistrationDigest  string            `json:"-"`
	PasswordLogin       bool              `json:"password_login"`
	PasswordHash        string            `json:"password_hash,omitempty"`
	DisplayName         string            `json:"display_name"`
	Bio                 string            `json:"bio,omitempty"`
	AvatarMediaID       int64             `json:"avatar_media_id,omitempty"`
	Status              string            `json:"status"`
	Locale              string            `json:"locale"`
	Version             int64             `json:"version"`
	Roles               []Role            `json:"roles"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	PasswordChangedAt   time.Time         `json:"password_changed_at"`
	LastLoginAt         time.Time         `json:"last_login_at,omitempty"`
	EmailVerifiedAt     *time.Time        `json:"email_verified_at,omitempty"`
}

type UserInput struct {
	ReferralHash   string
	ReferralPolicy ReferralPolicy
	// Account is optional so existing identity-only callers preserve profile and
	// contact verification fields. Administrative editors submit the full group.
	Account              *UserAccountInput
	RegistrationCodeHash string // Internal admission proof; never projected in users.
	Phone                string
	EmailVerified        bool
	PhoneVerified        bool
	ExternalKey          string
	OAuthProvider        string
	RegistrationKey      string
	RegistrationDigest   string
	DisablePassword      bool
	Username             string
	Email                string
	DisplayName          string
	Status               string
	Locale               string
	RoleSlugs            []string
}

type UserAccountInput struct {
	Bio           string
	AvatarMediaID int64
	Phone         string
	EmailVerified bool
	PhoneVerified bool
}

type UserMutation struct {
	ExpectedVersion int64
	Actor           string
}

type UserQuery struct {
	OAuthProvider string
	Search        string
	// PublicOnly excludes private email addresses from contact search.
	PublicOnly    bool
	ExcludeUserID int64
	AvatarMediaID int64
	Status        string
	Role          string
	Limit         int
	Offset        int
}

type UserList struct {
	Items        []User         `json:"items"`
	Total        int            `json:"total"`
	StatusCounts map[string]int `json:"status_counts"`
	RoleCounts   map[string]int `json:"role_counts"`
}

// UserStore is implemented by both the atomic local file store and PostgreSQL.
// Implementations must enforce optimistic versions, case-insensitive username
// and email uniqueness, role existence, and last-administrator protection.
type UserStore interface {
	Ready(context.Context) error
	EnsureRoles(context.Context, []Role) error
	CountManagedUsers(context.Context) (int, error)
	FindUserByLogin(context.Context, string) (User, error)
	GetUser(context.Context, int64) (User, error)
	ListUsers(context.Context, UserQuery) (UserList, error)
	ListRoles(context.Context) ([]Role, error)
	CreateUser(context.Context, UserMutation, UserInput, string) (User, error)
	UpdateUser(context.Context, int64, UserMutation, UserInput) (User, error)
	UpdateUserPassword(context.Context, int64, UserMutation, string) (User, error)
	RecordUserLogin(context.Context, int64, time.Time) error
}

// FirstAdminSetupStore is intentionally implemented only by the PostgreSQL
// repository. It exposes a durable, transactionally claimed installation gate.
type FirstAdminSetupStore interface {
	FirstAdminSetupAvailable(context.Context) (bool, error)
	ClaimFirstAdministrator(context.Context, UserMutation, UserInput, string) (User, error)
}

type UserValidationError struct {
	Fields map[string]string
}

func (err *UserValidationError) Error() string { return "user validation failed" }

func UserFieldErrors(err error) map[string]string {
	var validation *UserValidationError
	if !errors.As(err, &validation) {
		return nil
	}
	fields := make(map[string]string, len(validation.Fields))
	for key, value := range validation.Fields {
		fields[key] = value
	}
	return fields
}

func BuiltInRoles() []Role {
	return []Role{
		{
			Slug: "administrator", Name: "管理员", BuiltIn: true,
			Description:  "拥有全部管理权限，包括用户、设置、主题和内容。",
			Capabilities: []string{CapabilityAll},
		},
		{
			Slug: "editor", Name: "编辑", BuiltIn: true,
			Description: "管理内容、分类、媒体和评论，不能修改用户与设置。",
			Capabilities: []string{
				CapabilityAdminAccess, CapabilityDashboardRead, CapabilityContentRead, CapabilityContentWrite,
				CapabilityTaxonomyManage, CapabilityMediaRead, CapabilityMediaWrite,
				CapabilityCommentsModerate, CapabilitySystemRead, CapabilitySessionsRead, CapabilitySessionsOwn,
			},
		},
		{
			Slug: "author", Name: "作者", BuiltIn: true,
			Description: "可以进入写作后台，管理自己的文章。",
			Capabilities: []string{
				CapabilityAdminAccess, CapabilityDashboardRead, CapabilityContentRead,
				CapabilityContentWriteOwn, CapabilityMediaRead, CapabilityProfileWriteOwn,
				CapabilitySessionsRead, CapabilitySessionsOwn,
			},
		},
		{
			Slug: "subscriber", Name: "订阅者", BuiltIn: true,
			Description:  "仅维护自己的公开资料，不进入管理后台。",
			Capabilities: []string{CapabilityProfileWriteOwn},
		},
	}
}

func NormalizeUserInput(input UserInput) (UserInput, map[string]string) {
	fields := make(map[string]string)
	if (input.ExternalKey == "") != (input.OAuthProvider == "") || input.ExternalKey != "" && (!validRegistrationDigest(input.ExternalKey) || !validOAuthProvider(input.OAuthProvider)) {
		fields["oauth"] = "第三方身份或平台无效"
	}
	input.Username = strings.ToLower(strings.TrimSpace(input.Username))
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.Phone = strings.TrimSpace(input.Phone)
	if input.Phone != "" && !ValidPhone(input.Phone) {
		fields["phone"] = "手机号需包含国家区号，例如 +8613800138000"
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.Locale = strings.TrimSpace(input.Locale)
	if input.Status == "" {
		input.Status = UserStatusActive
	}
	if input.Locale == "" {
		input.Locale = "zh-CN"
	}
	if input.DisplayName == "" {
		input.DisplayName = input.Username
	}

	if !validUsername(input.Username) {
		fields["username"] = "用户名需为 3–80 个字母、数字、点、下划线或连字符，且不能包含空格"
	}
	if input.Email != "" && (len(input.Email) > 320 || !validEmail(input.Email)) {
		fields["email"] = "请输入有效且不超过 320 个字符的邮箱地址"
	}
	if !validUserText(input.DisplayName, 1, 160) {
		fields["display_name"] = "显示名称需为 1–160 个字符且不能包含控制字符"
	}
	if input.Status != UserStatusActive && input.Status != UserStatusSuspended {
		fields["status"] = "账号状态必须是启用或停用"
	}
	if len(input.Locale) > 35 || !localePattern.MatchString(input.Locale) {
		fields["locale"] = "语言标识必须是有效的 BCP 47 形式，例如 zh-CN"
	}

	roleSet := make(map[string]struct{}, len(input.RoleSlugs))
	roles := make([]string, 0, len(input.RoleSlugs))
	for _, role := range input.RoleSlugs {
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "" {
			continue
		}
		if !validRoleSlug(role) {
			fields["roles"] = "角色标识格式不正确"
			continue
		}
		if _, exists := roleSet[role]; exists {
			continue
		}
		roleSet[role] = struct{}{}
		roles = append(roles, role)
	}
	sort.Strings(roles)
	input.RoleSlugs = roles
	if len(input.RoleSlugs) == 0 {
		fields["roles"] = "至少选择一个角色"
	}
	if input.Account != nil {
		account := *input.Account
		profile, profileFields := NormalizeProfile(ProfileInput{DisplayName: input.DisplayName, Locale: input.Locale, Bio: account.Bio, AvatarMediaID: account.AvatarMediaID})
		for key, value := range profileFields {
			fields[key] = value
		}
		account.Bio = profile.Bio
		account.Phone = strings.TrimSpace(account.Phone)
		if account.Phone != "" && !ValidPhone(account.Phone) {
			fields["phone"] = "手机号需包含国家区号，例如 +8613800138000"
		}
		if account.EmailVerified && input.Email == "" {
			fields["email_verified"] = "请先填写邮箱，再设置验证状态"
		}
		if account.PhoneVerified && account.Phone == "" {
			fields["phone_verified"] = "请先填写手机号，再设置验证状态"
		}
		input.Account = &account
		input.Phone, input.EmailVerified, input.PhoneVerified = account.Phone, account.EmailVerified, account.PhoneVerified
	}
	return input, fields
}

func NormalizeUserQuery(query UserQuery) (UserQuery, error) {
	if query.OAuthProvider != "" && query.OAuthProvider != "bound" && query.OAuthProvider != "unbound" && !validOAuthProvider(query.OAuthProvider) {
		return UserQuery{}, errors.New("invalid OAuth provider filter")
	}
	if query.AvatarMediaID < 0 {
		return UserQuery{}, &UserValidationError{Fields: map[string]string{"avatar_media_id": "头像编号不能为负数"}}
	}
	query.Search = strings.TrimSpace(query.Search)
	if !utf8.ValidString(query.Search) {
		return UserQuery{}, &UserValidationError{Fields: map[string]string{"search": "搜索词必须是有效的 UTF-8 文本"}}
	}
	if utf8.RuneCountInString(query.Search) > maxUserSearchRunes || containsUserControl(query.Search) {
		return UserQuery{}, &UserValidationError{Fields: map[string]string{"search": fmt.Sprintf("搜索词不能超过 %d 个字符", maxUserSearchRunes)}}
	}
	query.Status = strings.ToLower(strings.TrimSpace(query.Status))
	if query.Status != "" && query.Status != UserStatusActive && query.Status != UserStatusSuspended {
		return UserQuery{}, &UserValidationError{Fields: map[string]string{"status": "账号状态筛选不受支持"}}
	}
	query.Role = strings.ToLower(strings.TrimSpace(query.Role))
	if query.Role != "" && !validRoleSlug(query.Role) {
		return UserQuery{}, &UserValidationError{Fields: map[string]string{"role": "角色筛选不受支持"}}
	}
	if query.Limit <= 0 || query.Limit > 100 {
		query.Limit = 20
	}
	if query.Offset < 0 {
		return UserQuery{}, &UserValidationError{Fields: map[string]string{"page": "分页偏移不能为负数"}}
	}
	return query, nil
}

func NormalizeRole(role Role) (Role, error) {
	return normalizedRole(role)
}

func (user User) Can(capability string) bool {
	capability = strings.TrimSpace(capability)
	if capability == "" {
		return false
	}
	for _, role := range user.Roles {
		if roleAllows(role.Capabilities, capability) {
			return true
		}
	}
	return false
}

func (user User) HasRole(slug string) bool {
	slug = strings.ToLower(strings.TrimSpace(slug))
	for _, role := range user.Roles {
		if role.Slug == slug {
			return true
		}
	}
	return false
}

func (user User) PrimaryRoleName() string {
	priority := []string{"administrator", "editor", "author", "subscriber"}
	for _, slug := range priority {
		for _, role := range user.Roles {
			if role.Slug == slug {
				return role.Name
			}
		}
	}
	if len(user.Roles) > 0 {
		return user.Roles[0].Name
	}
	return "无角色"
}

func roleAllows(capabilities []string, requested string) bool {
	if strings.TrimSpace(requested) == "" {
		return false
	}
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if capability == CapabilityAll || capability == requested {
			return true
		}
		if strings.HasSuffix(capability, ":*") && strings.HasPrefix(requested, strings.TrimSuffix(capability, "*")) {
			return true
		}
	}
	return false
}

func mergedCapabilities(roles []Role) []string {
	set := make(map[string]struct{})
	for _, role := range roles {
		for _, capability := range role.Capabilities {
			capability = strings.TrimSpace(capability)
			if capability != "" {
				set[capability] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(set))
	for capability := range set {
		result = append(result, capability)
	}
	sort.Strings(result)
	return result
}

func validUsername(value string) bool {
	count := utf8.RuneCountInString(value)
	if !utf8.ValidString(value) || count < 3 || count > 80 {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validEmail(value string) bool {
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && strings.EqualFold(parsed.Address, value) && strings.Contains(value, "@")
}

func validUserText(value string, minimum, maximum int) bool {
	count := utf8.RuneCountInString(value)
	return utf8.ValidString(value) && count >= minimum && count <= maximum && !containsUserControl(value)
}

func validRoleSlug(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func containsUserControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func cloneRoles(source []Role) []Role {
	result := make([]Role, len(source))
	for index, role := range source {
		result[index] = role
		result[index].Capabilities = append([]string(nil), role.Capabilities...)
	}
	return result
}

func cloneUser(user User) User {
	user.EmailVerifiedAt = cloneTime(user.EmailVerifiedAt)
	user.PhoneVerifiedAt = cloneTime(user.PhoneVerifiedAt)
	user.Roles = cloneRoles(user.Roles)
	return user
}
