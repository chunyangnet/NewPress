package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/auth"
)

type UserRepository struct {
	pool *pgxpool.Pool
}

func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

func (repository *UserRepository) Ready(ctx context.Context) error {
	_, err := repository.pool.Exec(ctx, `SELECT u.email_verified_at,u.bio,u.avatar_media_id,a.token_hash FROM np_users u CROSS JOIN np_account_access a LIMIT 0`)
	return err
}

func (repository *UserRepository) EnsureRoles(ctx context.Context, roles []auth.Role) error {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin role seed: %w", err)
	}
	defer tx.Rollback(ctx)
	for _, input := range roles {
		role, err := auth.NormalizeRole(input)
		if err != nil {
			return err
		}
		capabilities, err := json.Marshal(role.Capabilities)
		if err != nil {
			return fmt.Errorf("encode role capabilities: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO np_roles(slug,name,description,capabilities,built_in)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (slug) DO UPDATE SET
				name=EXCLUDED.name,
				description=EXCLUDED.description,
				capabilities=EXCLUDED.capabilities,
				built_in=EXCLUDED.built_in`,
			role.Slug, role.Name, role.Description, capabilities, role.BuiltIn,
		); err != nil {
			return fmt.Errorf("seed role %s: %w", role.Slug, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit role seed: %w", err)
	}
	return nil
}

func (repository *UserRepository) CountManagedUsers(ctx context.Context) (int, error) {
	var count int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM np_users WHERE status <> 'system'`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count managed users: %w", err)
	}
	return count, nil
}

func (repository *UserRepository) FirstAdminSetupAvailable(ctx context.Context) (bool, error) {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin first-admin setup check: %w", err)
	}
	defer tx.Rollback(ctx)
	var initialized bool
	if err := tx.QueryRow(ctx, `SELECT initialized FROM np_installation_state WHERE singleton=true`).Scan(&initialized); err != nil {
		return false, fmt.Errorf("read first-admin setup state: %w", err)
	}
	if !initialized {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM np_users WHERE status <> 'system'`).Scan(&count); err != nil {
			return false, fmt.Errorf("check first-admin setup users: %w", err)
		}
		if count > 0 {
			if _, err := tx.Exec(ctx, `UPDATE np_installation_state SET initialized=true, initialized_at=COALESCE(initialized_at,now()) WHERE singleton=true`); err != nil {
				return false, fmt.Errorf("close first-admin setup: %w", err)
			}
			initialized = true
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit first-admin setup check: %w", err)
	}
	return !initialized, nil
}

func (repository *UserRepository) ClaimFirstAdministrator(ctx context.Context, mutation auth.UserMutation, input auth.UserInput, passwordHash string) (auth.User, error) {
	normalized, fields := auth.NormalizeUserInput(input)
	if len(fields) > 0 {
		return auth.User{}, &auth.UserValidationError{Fields: fields}
	}
	if err := auth.ValidatePasswordHash(passwordHash); err != nil {
		return auth.User{}, &auth.UserValidationError{Fields: map[string]string{"password": "密码哈希格式不正确"}}
	}
	if normalized.Status != auth.UserStatusActive || len(normalized.RoleSlugs) != 1 || normalized.RoleSlugs[0] != "administrator" {
		return auth.User{}, &auth.UserValidationError{Fields: map[string]string{"roles": "首次认领必须创建启用的管理员账号"}}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return auth.User{}, fmt.Errorf("begin first-admin claim: %w", err)
	}
	defer tx.Rollback(ctx)
	var initialized bool
	if err := tx.QueryRow(ctx, `SELECT initialized FROM np_installation_state WHERE singleton=true FOR UPDATE`).Scan(&initialized); err != nil {
		return auth.User{}, fmt.Errorf("lock first-admin setup state: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM np_users WHERE status <> 'system'`).Scan(&count); err != nil {
		return auth.User{}, fmt.Errorf("check first-admin claim users: %w", err)
	}
	if initialized || count > 0 {
		if _, err := tx.Exec(ctx, `UPDATE np_installation_state SET initialized=true, initialized_at=COALESCE(initialized_at,now()) WHERE singleton=true`); err != nil {
			return auth.User{}, fmt.Errorf("close first-admin setup: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return auth.User{}, fmt.Errorf("commit closed first-admin setup: %w", err)
		}
		return auth.User{}, auth.ErrFirstAdminSetupClosed
	}
	roleIDs, err := resolveAuthRoleIDs(ctx, tx, normalized.RoleSlugs)
	if err != nil {
		return auth.User{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO np_users(username,email,password_hash,display_name,status,locale,version,created_at,updated_at,password_changed_at,phone,phone_verified_at,email_verified_at,registration_key,registration_digest,password_login,registration_version)
		VALUES ($1,$2,$3,$4,$5,$6,1,$7,$7,$7,'',NULL,NULL,'','',true,0)
		RETURNING id`, normalized.Username, normalized.Email, passwordHash, normalized.DisplayName, normalized.Status, normalized.Locale, now).Scan(&id)
	if err != nil {
		return auth.User{}, mapAuthUserWriteError("claim first administrator", err)
	}
	if err := replaceAuthUserRoles(ctx, tx, id, roleIDs); err != nil {
		return auth.User{}, err
	}
	if err := insertAuthAudit(ctx, tx, mutation, "user.created", id, map[string]any{"username": normalized.Username, "roles": normalized.RoleSlugs, "setup": "first-administrator-claim"}); err != nil {
		return auth.User{}, err
	}
	user, err := queryAuthUserByID(ctx, tx, id, false)
	if err != nil {
		return auth.User{}, fmt.Errorf("load claimed administrator: %w", err)
	}
	users := []auth.User{user}
	if err := hydrateUserRoles(ctx, tx, users); err != nil {
		return auth.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return auth.User{}, fmt.Errorf("commit first-admin claim: %w", err)
	}
	return users[0], nil
}

func (repository *UserRepository) FindUserByLogin(ctx context.Context, login string) (auth.User, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	user, err := scanAuthUser(repository.pool.QueryRow(ctx, `SELECT `+userColumns+`
		FROM np_users u WHERE u.status <> 'system' AND (lower(u.username)=$1 OR (u.email<>'' AND lower(u.email)=$1) OR (u.phone<>'' AND u.phone=$1))`, login))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("find user by login: %w", err)
	}
	users := []auth.User{user}
	if err := hydrateUserRoles(ctx, repository.pool, users); err != nil {
		return auth.User{}, err
	}
	return users[0], nil
}

func (repository *UserRepository) GetUser(ctx context.Context, id int64) (auth.User, error) {
	user, err := queryAuthUserByID(ctx, repository.pool, id, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("get user: %w", err)
	}
	if user.Status == auth.UserStatusSystem {
		return auth.User{}, auth.ErrUserNotFound
	}
	users := []auth.User{user}
	if err := hydrateUserRoles(ctx, repository.pool, users); err != nil {
		return auth.User{}, err
	}
	return users[0], nil
}

func (repository *UserRepository) ListUsers(ctx context.Context, query auth.UserQuery) (auth.UserList, error) {
	normalized, err := auth.NormalizeUserQuery(query)
	if err != nil {
		return auth.UserList{}, err
	}
	where, args := userWhere(normalized)
	var total int
	if err := repository.pool.QueryRow(ctx, `SELECT count(*) FROM np_users u `+where, args...).Scan(&total); err != nil {
		return auth.UserList{}, fmt.Errorf("count users: %w", err)
	}

	itemArgs := append([]any(nil), args...)
	limitPlaceholder := len(itemArgs) + 1
	offsetPlaceholder := len(itemArgs) + 2
	itemArgs = append(itemArgs, normalized.Limit, normalized.Offset)
	rows, err := repository.pool.Query(ctx, `SELECT `+userColumns+` FROM np_users u `+where+`
		ORDER BY u.updated_at DESC,u.id DESC LIMIT $`+fmt.Sprint(limitPlaceholder)+` OFFSET $`+fmt.Sprint(offsetPlaceholder), itemArgs...)
	if err != nil {
		return auth.UserList{}, fmt.Errorf("list users: %w", err)
	}
	users := make([]auth.User, 0, min(normalized.Limit, total))
	for rows.Next() {
		user, scanErr := scanAuthUser(rows)
		if scanErr != nil {
			rows.Close()
			return auth.UserList{}, fmt.Errorf("scan user: %w", scanErr)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return auth.UserList{}, fmt.Errorf("iterate users: %w", err)
	}
	rows.Close()
	if err := hydrateUserRoles(ctx, repository.pool, users); err != nil {
		return auth.UserList{}, err
	}

	statusCounts := make(map[string]int)
	statusRows, err := repository.pool.Query(ctx, `SELECT status,count(*) FROM np_users WHERE status <> 'system' GROUP BY status`)
	if err != nil {
		return auth.UserList{}, fmt.Errorf("count user statuses: %w", err)
	}
	for statusRows.Next() {
		var status string
		var count int
		if err := statusRows.Scan(&status, &count); err != nil {
			statusRows.Close()
			return auth.UserList{}, err
		}
		statusCounts[status] = count
	}
	if err := statusRows.Err(); err != nil {
		statusRows.Close()
		return auth.UserList{}, err
	}
	statusRows.Close()

	roleCounts := make(map[string]int)
	roleRows, err := repository.pool.Query(ctx, `SELECT r.slug,count(*) FROM np_user_roles ur JOIN np_roles r ON r.id=ur.role_id JOIN np_users u ON u.id=ur.user_id WHERE u.status <> 'system' GROUP BY r.slug`)
	if err != nil {
		return auth.UserList{}, fmt.Errorf("count user roles: %w", err)
	}
	for roleRows.Next() {
		var slug string
		var count int
		if err := roleRows.Scan(&slug, &count); err != nil {
			roleRows.Close()
			return auth.UserList{}, err
		}
		roleCounts[slug] = count
	}
	if err := roleRows.Err(); err != nil {
		roleRows.Close()
		return auth.UserList{}, err
	}
	roleRows.Close()
	return auth.UserList{Items: users, Total: total, StatusCounts: statusCounts, RoleCounts: roleCounts}, nil
}

func (repository *UserRepository) ListRoles(ctx context.Context) ([]auth.Role, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT r.id,r.slug,r.name,r.description,r.capabilities,r.built_in,count(u.id)
		FROM np_roles r
		LEFT JOIN np_user_roles ur ON ur.role_id=r.id
		LEFT JOIN np_users u ON u.id=ur.user_id AND u.status <> 'system'
		GROUP BY r.id,r.slug,r.name,r.description,r.capabilities,r.built_in`)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()
	roles := make([]auth.Role, 0, 8)
	for rows.Next() {
		role, err := scanAuthRole(rows, true)
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortAuthRoles(roles)
	return roles, nil
}

func (repository *UserRepository) CreateUser(ctx context.Context, mutation auth.UserMutation, input auth.UserInput, passwordHash string) (auth.User, error) {
	normalized, fields := auth.NormalizeUserInput(input)
	if len(fields) > 0 {
		return auth.User{}, &auth.UserValidationError{Fields: fields}
	}
	if err := auth.ValidatePasswordHash(passwordHash); err != nil {
		return auth.User{}, &auth.UserValidationError{Fields: map[string]string{"password": "密码哈希格式不正确"}}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return auth.User{}, fmt.Errorf("begin create user: %w", err)
	}
	defer tx.Rollback(ctx)
	roleIDs, err := resolveAuthRoleIDs(ctx, tx, normalized.RoleSlugs)
	if err != nil {
		return auth.User{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO np_users(username,email,password_hash,display_name,status,locale,version,created_at,updated_at,password_changed_at,phone,phone_verified_at,email_verified_at,registration_key,registration_digest,password_login,registration_version)
		VALUES ($1,$2,$3,$4,$5,$6,1,$7,$7,$7,$8,CASE WHEN $9 THEN $7::timestamptz ELSE NULL END,CASE WHEN $10 THEN $7::timestamptz ELSE NULL END,$11,$12,$13,CASE WHEN $11='' THEN 0 ELSE 1 END)
		RETURNING id`,
		normalized.Username, normalized.Email, passwordHash, normalized.DisplayName, normalized.Status, normalized.Locale, now, normalized.Phone, normalized.PhoneVerified, normalized.EmailVerified, normalized.RegistrationKey, normalized.RegistrationDigest, !normalized.DisablePassword,
	).Scan(&id)
	if err != nil {
		return auth.User{}, mapAuthUserWriteError("create user", err)
	}
	if err := consumeRegistrationCode(ctx, tx, normalized.RegistrationCodeHash); err != nil {
		return auth.User{}, err
	}
	if normalized.ExternalKey != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO np_oauth_bindings(user_id,provider,external_key) VALUES($1,$2,$3)`, id, normalized.OAuthProvider, normalized.ExternalKey); err != nil {
			return auth.User{}, mapAuthUserWriteError("create OAuth binding", err)
		}
	}
	if err := consumeReferral(ctx, tx, id, normalized); err != nil {
		return auth.User{}, err
	}
	if account := normalized.Account; account != nil {
		if _, err := tx.Exec(ctx, `UPDATE np_users SET bio=$2,avatar_media_id=$3 WHERE id=$1`, id, account.Bio, account.AvatarMediaID); err != nil {
			return auth.User{}, mapAuthUserWriteError("create user profile", err)
		}
	}
	if err := replaceAuthUserRoles(ctx, tx, id, roleIDs); err != nil {
		return auth.User{}, err
	}
	if err := insertAuthAudit(ctx, tx, mutation, "user.created", id, map[string]any{"username": normalized.Username, "roles": normalized.RoleSlugs}); err != nil {
		return auth.User{}, err
	}
	user, err := queryAuthUserByID(ctx, tx, id, false)
	if err != nil {
		return auth.User{}, fmt.Errorf("load created user: %w", err)
	}
	users := []auth.User{user}
	if err := hydrateUserRoles(ctx, tx, users); err != nil {
		return auth.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return auth.User{}, fmt.Errorf("commit create user: %w", err)
	}
	return users[0], nil
}

func (repository *UserRepository) UpdateUser(ctx context.Context, id int64, mutation auth.UserMutation, input auth.UserInput) (auth.User, error) {
	normalized, fields := auth.NormalizeUserInput(input)
	if len(fields) > 0 {
		return auth.User{}, &auth.UserValidationError{Fields: fields}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return auth.User{}, fmt.Errorf("begin update user: %w", err)
	}
	defer tx.Rollback(ctx)
	current, err := queryAuthUserByID(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) || current.Status == auth.UserStatusSystem {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("lock user: %w", err)
	}
	if mutation.ExpectedVersion <= 0 || current.Version != mutation.ExpectedVersion {
		return auth.User{}, auth.ErrUserVersionConflict
	}
	currentUsers := []auth.User{current}
	if err := hydrateUserRoles(ctx, tx, currentUsers); err != nil {
		return auth.User{}, err
	}
	current = currentUsers[0]
	roleIDs, err := resolveAuthRoleIDs(ctx, tx, normalized.RoleSlugs)
	if err != nil {
		return auth.User{}, err
	}
	losingAdministrator := current.Status == auth.UserStatusActive && current.HasRole("administrator") &&
		(normalized.Status != auth.UserStatusActive || !containsAuthString(normalized.RoleSlugs, "administrator"))
	if losingAdministrator {
		// Serialize every administrator-removal decision on the role row. Without
		// this lock two active administrators could concurrently observe a count
		// of two and both demote themselves, violating the invariant at commit.
		var administratorRoleID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM np_roles WHERE slug='administrator' FOR UPDATE`).Scan(&administratorRoleID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.User{}, auth.ErrRoleNotFound
			}
			return auth.User{}, fmt.Errorf("lock administrator role: %w", err)
		}
		var administratorCount int
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT u.id) FROM np_users u JOIN np_user_roles ur ON ur.user_id=u.id JOIN np_roles r ON r.id=ur.role_id WHERE u.status='active' AND r.slug='administrator'`).Scan(&administratorCount); err != nil {
			return auth.User{}, fmt.Errorf("count active administrators: %w", err)
		}
		if administratorCount <= 1 {
			return auth.User{}, auth.ErrLastAdministrator
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	command, err := tx.Exec(ctx, `
		UPDATE np_users SET username=$2,email_verified_at=CASE WHEN email=$3 THEN email_verified_at ELSE NULL END,email=$3,display_name=$4,status=$5,locale=$6,updated_at=$7,version=version+1
		WHERE id=$1 AND version=$8`,
		id, normalized.Username, normalized.Email, normalized.DisplayName, normalized.Status, normalized.Locale, now, mutation.ExpectedVersion,
	)
	if err != nil {
		return auth.User{}, mapAuthUserWriteError("update user", err)
	}
	if command.RowsAffected() != 1 {
		return auth.User{}, auth.ErrUserVersionConflict
	}
	if account := normalized.Account; account != nil {
		_, err = tx.Exec(ctx, `UPDATE np_users SET bio=$2,avatar_media_id=$3,
			phone_verified_at=CASE WHEN NOT $5 THEN NULL WHEN phone=$4 AND phone_verified_at IS NOT NULL THEN phone_verified_at ELSE $7 END,
			phone=$4,email_verified_at=CASE WHEN NOT $6 THEN NULL ELSE COALESCE(email_verified_at,$7) END
			WHERE id=$1`, id, account.Bio, account.AvatarMediaID, account.Phone, account.PhoneVerified, account.EmailVerified, now)
		if err != nil {
			return auth.User{}, mapAuthUserWriteError("update user account", err)
		}
	}
	if err := replaceAuthUserRoles(ctx, tx, id, roleIDs); err != nil {
		return auth.User{}, err
	}
	if err := insertAuthAudit(ctx, tx, mutation, "user.updated", id, map[string]any{"username": normalized.Username, "status": normalized.Status, "roles": normalized.RoleSlugs}); err != nil {
		return auth.User{}, err
	}
	user, err := queryAuthUserByID(ctx, tx, id, false)
	if err != nil {
		return auth.User{}, fmt.Errorf("load updated user: %w", err)
	}
	users := []auth.User{user}
	if err := hydrateUserRoles(ctx, tx, users); err != nil {
		return auth.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return auth.User{}, fmt.Errorf("commit update user: %w", err)
	}
	return users[0], nil
}

func (repository *UserRepository) UpdateUserPassword(ctx context.Context, id int64, mutation auth.UserMutation, passwordHash string) (auth.User, error) {
	if err := auth.ValidatePasswordHash(passwordHash); err != nil {
		return auth.User{}, &auth.UserValidationError{Fields: map[string]string{"password": "密码哈希格式不正确"}}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return auth.User{}, fmt.Errorf("begin update user password: %w", err)
	}
	defer tx.Rollback(ctx)
	current, err := queryAuthUserByID(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) || current.Status == auth.UserStatusSystem {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("lock user password: %w", err)
	}
	if mutation.ExpectedVersion <= 0 || current.Version != mutation.ExpectedVersion {
		return auth.User{}, auth.ErrUserVersionConflict
	}
	now := time.Now().UTC().Truncate(time.Second)
	command, err := tx.Exec(ctx, `UPDATE np_users SET password_hash=$2,password_login=true,password_changed_at=$3,updated_at=$3,version=version+1 WHERE id=$1 AND version=$4`,
		id, passwordHash, now, mutation.ExpectedVersion)
	if err != nil {
		return auth.User{}, fmt.Errorf("update user password: %w", err)
	}
	if command.RowsAffected() != 1 {
		return auth.User{}, auth.ErrUserVersionConflict
	}
	if err := insertAuthAudit(ctx, tx, mutation, "user.password_changed", id, nil); err != nil {
		return auth.User{}, err
	}
	user, err := queryAuthUserByID(ctx, tx, id, false)
	if err != nil {
		return auth.User{}, fmt.Errorf("load password-updated user: %w", err)
	}
	users := []auth.User{user}
	if err := hydrateUserRoles(ctx, tx, users); err != nil {
		return auth.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return auth.User{}, fmt.Errorf("commit user password: %w", err)
	}
	return users[0], nil
}

func (repository *UserRepository) RecordUserLogin(ctx context.Context, id int64, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	command, err := repository.pool.Exec(ctx, `UPDATE np_users SET last_login_at=$2 WHERE id=$1 AND status <> 'system'`, id, at.UTC().Truncate(time.Second))
	if err != nil {
		return fmt.Errorf("record user login: %w", err)
	}
	if command.RowsAffected() != 1 {
		return auth.ErrUserNotFound
	}
	return nil
}

const userColumns = `
	u.id,u.username,u.email,u.password_hash,u.display_name,u.status,u.locale,u.version,
	u.created_at,u.updated_at,u.password_changed_at,u.last_login_at,u.email_verified_at,u.bio,u.avatar_media_id,u.phone,u.phone_verified_at,u.registration_key,u.registration_digest,u.password_login,u.registration_version,u.referral,
	COALESCE((SELECT jsonb_object_agg(b.provider,b.external_key) FROM np_oauth_bindings b WHERE b.user_id=u.id), '{}'::jsonb)`

func scanAuthUser(row scanner) (auth.User, error) {
	var user auth.User
	var lastLogin *time.Time
	if err := row.Scan(
		&user.ID, &user.Username, &user.Email, &user.PasswordHash, &user.DisplayName, &user.Status,
		&user.Locale, &user.Version, &user.CreatedAt, &user.UpdatedAt, &user.PasswordChangedAt, &lastLogin, &user.EmailVerifiedAt,
		&user.Bio, &user.AvatarMediaID, &user.Phone, &user.PhoneVerifiedAt, &user.RegistrationKey, &user.RegistrationDigest, &user.PasswordLogin, &user.RegistrationVersion,
		&user.Referral,
		&user.OAuthBindings,
	); err != nil {
		return auth.User{}, err
	}
	if lastLogin != nil {
		user.LastLoginAt = *lastLogin
	}
	return user, nil
}

type authUserQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func queryAuthUserByID(ctx context.Context, querier authUserQuerier, id int64, forUpdate bool) (auth.User, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	return scanAuthUser(querier.QueryRow(ctx, `SELECT `+userColumns+` FROM np_users u WHERE u.id=$1`+suffix, id))
}

func hydrateUserRoles(ctx context.Context, querier authUserQuerier, users []auth.User) error {
	if len(users) == 0 {
		return nil
	}
	ids := make([]int64, len(users))
	indexes := make(map[int64]int, len(users))
	for index := range users {
		ids[index] = users[index].ID
		indexes[users[index].ID] = index
		users[index].Roles = nil
	}
	rows, err := querier.Query(ctx, `
		SELECT ur.user_id,r.id,r.slug,r.name,r.description,r.capabilities,r.built_in
		FROM np_user_roles ur JOIN np_roles r ON r.id=ur.role_id
		WHERE ur.user_id=ANY($1)
		ORDER BY ur.user_id,r.slug`, ids)
	if err != nil {
		return fmt.Errorf("load user roles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		var role auth.Role
		var capabilities []byte
		if err := rows.Scan(&userID, &role.ID, &role.Slug, &role.Name, &role.Description, &capabilities, &role.BuiltIn); err != nil {
			return fmt.Errorf("scan user role: %w", err)
		}
		if err := json.Unmarshal(capabilities, &role.Capabilities); err != nil {
			return fmt.Errorf("decode user role capabilities: %w", err)
		}
		if index, ok := indexes[userID]; ok {
			users[index].Roles = append(users[index].Roles, role)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range users {
		sortAuthRoles(users[index].Roles)
	}
	return nil
}

func scanAuthRole(row scanner, withCount bool) (auth.Role, error) {
	var role auth.Role
	var capabilities []byte
	var err error
	if withCount {
		err = row.Scan(&role.ID, &role.Slug, &role.Name, &role.Description, &capabilities, &role.BuiltIn, &role.UserCount)
	} else {
		err = row.Scan(&role.ID, &role.Slug, &role.Name, &role.Description, &capabilities, &role.BuiltIn)
	}
	if err != nil {
		return auth.Role{}, err
	}
	if err := json.Unmarshal(capabilities, &role.Capabilities); err != nil {
		return auth.Role{}, fmt.Errorf("decode role capabilities: %w", err)
	}
	return role, nil
}

func resolveAuthRoleIDs(ctx context.Context, tx pgx.Tx, slugs []string) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT id,slug FROM np_roles WHERE slug=ANY($1)`, slugs)
	if err != nil {
		return nil, fmt.Errorf("resolve user roles: %w", err)
	}
	defer rows.Close()
	idsBySlug := make(map[string]int64, len(slugs))
	for rows.Next() {
		var id int64
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return nil, err
		}
		idsBySlug[slug] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(slugs))
	for _, slug := range slugs {
		id, ok := idsBySlug[slug]
		if !ok {
			return nil, fmt.Errorf("%w: %s", auth.ErrRoleNotFound, slug)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func replaceAuthUserRoles(ctx context.Context, tx pgx.Tx, userID int64, roleIDs []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM np_user_roles WHERE user_id=$1`, userID); err != nil {
		return fmt.Errorf("clear user roles: %w", err)
	}
	for _, roleID := range roleIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO np_user_roles(user_id,role_id) VALUES ($1,$2)`, userID, roleID); err != nil {
			return fmt.Errorf("assign user role: %w", err)
		}
	}
	return nil
}

func userWhere(query auth.UserQuery) (string, []any) {
	conditions := []string{"u.status <> 'system'"}
	args := make([]any, 0, 3)
	add := func(format string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(format, len(args)))
	}
	if query.Status != "" {
		add("u.status=$%d", query.Status)
	}
	if query.Role != "" {
		add("EXISTS (SELECT 1 FROM np_user_roles ur JOIN np_roles r ON r.id=ur.role_id WHERE ur.user_id=u.id AND r.slug=$%d)", query.Role)
	}
	switch query.OAuthProvider {
	case "":
	case "bound":
		conditions = append(conditions, "EXISTS (SELECT 1 FROM np_oauth_bindings b WHERE b.user_id=u.id)")
	case "unbound":
		conditions = append(conditions, "NOT EXISTS (SELECT 1 FROM np_oauth_bindings b WHERE b.user_id=u.id)")
	default:
		add("EXISTS (SELECT 1 FROM np_oauth_bindings b WHERE b.user_id=u.id AND b.provider=$%d)", query.OAuthProvider)
	}
	if query.ExcludeUserID > 0 {
		add("u.id<>$%d", query.ExcludeUserID)
	}
	if query.AvatarMediaID > 0 {
		add("u.avatar_media_id=$%d", query.AvatarMediaID)
	}
	if query.Search != "" {
		args = append(args, query.Search)
		if query.PublicOnly {
			conditions = append(conditions, fmt.Sprintf("(strpos(lower(u.username),lower($%[1]d))>0 OR strpos(lower(u.display_name),lower($%[1]d))>0)", len(args)))
		} else {
			conditions = append(conditions, fmt.Sprintf("(u.username ILIKE '%%' || $%[1]d || '%%' OR u.email ILIKE '%%' || $%[1]d || '%%' OR u.display_name ILIKE '%%' || $%[1]d || '%%')", len(args)))
		}
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

func insertAuthAudit(ctx context.Context, tx pgx.Tx, mutation auth.UserMutation, action string, userID int64, metadata map[string]any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata) VALUES ($1,$2,'user',$3,$4)`,
		strings.TrimSpace(mutation.Actor), action, fmt.Sprint(userID), metadata); err != nil {
		return fmt.Errorf("record user audit: %w", err)
	}
	return nil
}

func mapAuthUserWriteError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		constraint := strings.ToLower(postgresError.ConstraintName)
		switch {
		case strings.Contains(constraint, "username"):
			return auth.ErrUsernameConflict
		case strings.Contains(constraint, "phone"):
			return auth.ErrPhoneConflict
		case strings.Contains(constraint, "external_key"), strings.Contains(constraint, "registration_key"):
			return auth.ErrRegistrationConflict
		case strings.Contains(constraint, "email"):
			return auth.ErrEmailConflict
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func containsAuthString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func sortAuthRoles(roles []auth.Role) {
	priority := map[string]int{"administrator": 0, "editor": 1, "author": 2, "subscriber": 3}
	sort.Slice(roles, func(left, right int) bool {
		leftPriority, leftKnown := priority[roles[left].Slug]
		rightPriority, rightKnown := priority[roles[right].Slug]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown && leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return roles[left].Slug < roles[right].Slug
	})
}

var _ auth.UserStore = (*UserRepository)(nil)
