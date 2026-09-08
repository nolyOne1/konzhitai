package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const memberProjection = `
	u.id::text, u.email, u.display_name, u.enabled,
	u.must_change_password, u.removed_at, u.created_at,
	COALESCE(array_agg(role.name ORDER BY role.name)
		FILTER (WHERE role.name IS NOT NULL), ARRAY[]::text[])
`

type memberScanner interface {
	Scan(dest ...any) error
}

type lockedMember struct {
	enabled   bool
	removed   bool
	adminRole bool
}

func (r *PostgresRepository) ListMembers(ctx context.Context, status MemberStatus) ([]Member, error) {
	var condition string
	switch status {
	case MemberStatusActive:
		condition = "u.removed_at IS NULL AND u.enabled=true"
	case MemberStatusDisabled:
		condition = "u.removed_at IS NULL AND u.enabled=false"
	case MemberStatusRemoved:
		condition = "u.removed_at IS NOT NULL"
	case MemberStatusAll:
		condition = "u.removed_at IS NULL"
	default:
		return nil, ErrInvalidMember
	}
	rows, err := r.db.Query(ctx, `
		SELECT `+memberProjection+`
		FROM users AS u
		LEFT JOIN user_roles AS user_role ON user_role.user_id=u.id
		LEFT JOIN roles AS role ON role.id=user_role.role_id
		WHERE `+condition+`
		GROUP BY u.id ORDER BY u.email, u.id
	`)
	if err != nil {
		return nil, fmt.Errorf("读取团队成员：%w", err)
	}
	defer rows.Close()
	members := []Member{}
	for rows.Next() {
		member, err := scanMember(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描团队成员：%w", err)
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历团队成员：%w", err)
	}
	return members, nil
}

func (r *PostgresRepository) CreateMember(ctx context.Context, actorID string, record CreateMemberRecord) (Member, error) {
	actorID, err := NormalizeUserID(actorID)
	if err != nil {
		return Member{}, err
	}
	roles, err := normalizeRoles(record.Roles)
	if err != nil {
		return Member{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Member{}, fmt.Errorf("开始创建成员事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ensureRoles(ctx, tx, roles); err != nil {
		return Member{}, err
	}
	var targetID string
	err = tx.QueryRow(ctx, `
		INSERT INTO users (email,display_name,password_hash,must_change_password)
		VALUES ($1,$2,$3,true) RETURNING id::text
	`, record.Email, record.DisplayName, record.PasswordHash).Scan(&targetID)
	if err != nil {
		if duplicateEmailError(err) {
			return Member{}, ErrDuplicateEmail
		}
		return Member{}, fmt.Errorf("创建成员：%w", err)
	}
	if err := replaceRoles(ctx, tx, targetID, roles); err != nil {
		return Member{}, err
	}
	if err := insertMemberAudit(ctx, tx, actorID, "member.create", targetID, map[string]any{
		"email": record.Email, "displayName": record.DisplayName, "roles": roleStrings(roles),
	}); err != nil {
		return Member{}, err
	}
	member, err := loadMember(ctx, tx, targetID)
	if err != nil {
		return Member{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, fmt.Errorf("提交创建成员事务：%w", err)
	}
	return member, nil
}

func (r *PostgresRepository) ReplaceMemberRoles(ctx context.Context, actorID, targetID string, requestedRoles []RoleName) (Member, error) {
	actorID, targetID, err := normalizeMemberMutationIDs(actorID, targetID)
	if err != nil {
		return Member{}, err
	}
	roles, err := normalizeRoles(requestedRoles)
	if err != nil {
		return Member{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Member{}, fmt.Errorf("开始替换成员角色事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockAdminMutation(ctx, tx); err != nil {
		return Member{}, err
	}
	state, err := lockMember(ctx, tx, targetID)
	if err != nil {
		return Member{}, err
	}
	if state.removed {
		return Member{}, ErrMemberStateConflict
	}
	if state.enabled && state.adminRole && !containsRole(roles, RoleAdmin) {
		if err := preserveLastAdmin(ctx, tx); err != nil {
			return Member{}, err
		}
	}
	if err := ensureRoles(ctx, tx, roles); err != nil {
		return Member{}, err
	}
	if err := replaceRoles(ctx, tx, targetID, roles); err != nil {
		return Member{}, err
	}
	if err := insertMemberAudit(ctx, tx, actorID, "member.roles.update", targetID, map[string]any{"roles": roleStrings(roles)}); err != nil {
		return Member{}, err
	}
	member, err := loadMember(ctx, tx, targetID)
	if err != nil {
		return Member{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, fmt.Errorf("提交替换成员角色事务：%w", err)
	}
	return member, nil
}

func (r *PostgresRepository) SetMemberEnabled(ctx context.Context, actorID, targetID string, enabled bool) (Member, error) {
	actorID, targetID, err := normalizeMemberMutationIDs(actorID, targetID)
	if err != nil {
		return Member{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Member{}, fmt.Errorf("开始更新成员状态事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockAdminMutation(ctx, tx); err != nil {
		return Member{}, err
	}
	state, err := lockMember(ctx, tx, targetID)
	if err != nil {
		return Member{}, err
	}
	if state.removed || state.enabled == enabled {
		return Member{}, ErrMemberStateConflict
	}
	if !enabled && state.adminRole {
		if err := preserveLastAdmin(ctx, tx); err != nil {
			return Member{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET enabled=$2,updated_at=now() WHERE id=$1`, targetID, enabled); err != nil {
		return Member{}, fmt.Errorf("更新成员状态：%w", err)
	}
	action := "member.enable"
	if !enabled {
		action = "member.disable"
		if err := revokeMemberSessions(ctx, tx, targetID); err != nil {
			return Member{}, err
		}
	}
	if err := insertMemberAudit(ctx, tx, actorID, action, targetID, map[string]any{"enabled": enabled}); err != nil {
		return Member{}, err
	}
	member, err := loadMember(ctx, tx, targetID)
	if err != nil {
		return Member{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, fmt.Errorf("提交更新成员状态事务：%w", err)
	}
	return member, nil
}

func (r *PostgresRepository) RemoveMember(ctx context.Context, actorID, targetID string) (Member, error) {
	actorID, targetID, err := normalizeMemberMutationIDs(actorID, targetID)
	if err != nil {
		return Member{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Member{}, fmt.Errorf("开始移除成员事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockAdminMutation(ctx, tx); err != nil {
		return Member{}, err
	}
	state, err := lockMember(ctx, tx, targetID)
	if err != nil {
		return Member{}, err
	}
	if state.removed {
		return Member{}, ErrMemberStateConflict
	}
	if state.enabled && state.adminRole {
		if err := preserveLastAdmin(ctx, tx); err != nil {
			return Member{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET enabled=false,removed_at=now(),updated_at=now() WHERE id=$1`, targetID); err != nil {
		return Member{}, fmt.Errorf("移除成员：%w", err)
	}
	if err := revokeMemberSessions(ctx, tx, targetID); err != nil {
		return Member{}, err
	}
	if err := insertMemberAudit(ctx, tx, actorID, "member.remove", targetID, map[string]any{"enabled": false}); err != nil {
		return Member{}, err
	}
	member, err := loadMember(ctx, tx, targetID)
	if err != nil {
		return Member{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, fmt.Errorf("提交移除成员事务：%w", err)
	}
	return member, nil
}

func (r *PostgresRepository) RestoreMember(ctx context.Context, actorID, targetID string) (Member, error) {
	actorID, targetID, err := normalizeMemberMutationIDs(actorID, targetID)
	if err != nil {
		return Member{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Member{}, fmt.Errorf("开始恢复成员事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	state, err := lockMember(ctx, tx, targetID)
	if err != nil {
		return Member{}, err
	}
	if !state.removed {
		return Member{}, ErrMemberStateConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET enabled=false,removed_at=NULL,updated_at=now() WHERE id=$1`, targetID); err != nil {
		return Member{}, fmt.Errorf("恢复成员：%w", err)
	}
	if err := insertMemberAudit(ctx, tx, actorID, "member.restore", targetID, map[string]any{"enabled": false}); err != nil {
		return Member{}, err
	}
	member, err := loadMember(ctx, tx, targetID)
	if err != nil {
		return Member{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, fmt.Errorf("提交恢复成员事务：%w", err)
	}
	return member, nil
}

func (r *PostgresRepository) ResetMemberPassword(ctx context.Context, actorID, targetID, passwordHash string) (Member, error) {
	actorID, targetID, err := normalizeMemberMutationIDs(actorID, targetID)
	if err != nil {
		return Member{}, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Member{}, fmt.Errorf("开始重置成员密码事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	state, err := lockMember(ctx, tx, targetID)
	if err != nil {
		return Member{}, err
	}
	if state.removed {
		return Member{}, ErrMemberStateConflict
	}
	if _, err := tx.Exec(ctx, `
		UPDATE users SET password_hash=$2,must_change_password=true,updated_at=now() WHERE id=$1
	`, targetID, passwordHash); err != nil {
		return Member{}, fmt.Errorf("重置成员密码：%w", err)
	}
	if err := revokeMemberSessions(ctx, tx, targetID); err != nil {
		return Member{}, err
	}
	if err := insertMemberAudit(ctx, tx, actorID, "member.password.reset", targetID, map[string]any{"mustChangePassword": true}); err != nil {
		return Member{}, err
	}
	member, err := loadMember(ctx, tx, targetID)
	if err != nil {
		return Member{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Member{}, fmt.Errorf("提交重置成员密码事务：%w", err)
	}
	return member, nil
}

func scanMember(row memberScanner) (Member, error) {
	var member Member
	var roles []string
	err := row.Scan(
		&member.ID, &member.Email, &member.DisplayName, &member.Enabled,
		&member.MustChangePassword, &member.RemovedAt, &member.CreatedAt, &roles,
	)
	member.Roles = toRoleNames(roles)
	return member, err
}

func loadMember(ctx context.Context, tx pgx.Tx, targetID string) (Member, error) {
	member, err := scanMember(tx.QueryRow(ctx, `
		SELECT `+memberProjection+`
		FROM users AS u
		LEFT JOIN user_roles AS user_role ON user_role.user_id=u.id
		LEFT JOIN roles AS role ON role.id=user_role.role_id
		WHERE u.id=$1 GROUP BY u.id
	`, targetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrMemberNotFound
	}
	if err != nil {
		return Member{}, fmt.Errorf("读取成员投影：%w", err)
	}
	return member, nil
}

func lockMember(ctx context.Context, tx pgx.Tx, targetID string) (lockedMember, error) {
	var state lockedMember
	var removedAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT u.enabled,u.removed_at,
		       EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=u.id AND r.name='admin')
		FROM users u WHERE u.id=$1 FOR UPDATE
	`, targetID).Scan(&state.enabled, &removedAt, &state.adminRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedMember{}, ErrMemberNotFound
	}
	if err != nil {
		return lockedMember{}, fmt.Errorf("锁定成员：%w", err)
	}
	state.removed = removedAt != nil
	return state, nil
}

func preserveLastAdmin(ctx context.Context, tx pgx.Tx) error {
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(DISTINCT u.id)
		FROM users u
		JOIN user_roles ur ON ur.user_id=u.id
		JOIN roles r ON r.id=ur.role_id
		WHERE u.enabled=true AND u.removed_at IS NULL AND r.name='admin'
	`).Scan(&count); err != nil {
		return fmt.Errorf("统计有效管理员：%w", err)
	}
	if count <= 1 {
		return ErrLastAdmin
	}
	return nil
}

func lockAdminMutation(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('yunling-member-admin'))`); err != nil {
		return fmt.Errorf("锁定管理员变更：%w", err)
	}
	return nil
}

func normalizeMemberMutationIDs(actorID, targetID string) (string, string, error) {
	actorID, err := NormalizeUserID(actorID)
	if err != nil {
		return "", "", err
	}
	targetID, err = NormalizeUserID(targetID)
	if err != nil {
		return "", "", err
	}
	if actorID == targetID {
		return "", "", ErrCannotModifySelf
	}
	return actorID, targetID, nil
}

func ensureRoles(ctx context.Context, tx pgx.Tx, roles []RoleName) error {
	for _, role := range roles {
		permissions, ok := permissionsForRole(role)
		if !ok {
			return ErrInvalidRoles
		}
		encodedPermissions, err := json.Marshal(permissions)
		if err != nil {
			return fmt.Errorf("编码角色权限：%w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO roles (name,permissions) VALUES ($1,$2)
			ON CONFLICT (name) DO NOTHING
		`, role, encodedPermissions); err != nil {
			return fmt.Errorf("准备成员角色：%w", err)
		}
	}
	return nil
}

func replaceRoles(ctx context.Context, tx pgx.Tx, targetID string, roles []RoleName) error {
	if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id=$1`, targetID); err != nil {
		return fmt.Errorf("清除成员角色：%w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_roles (user_id,role_id)
		SELECT $1,id FROM roles WHERE name=ANY($2::text[])
	`, targetID, roleStrings(roles)); err != nil {
		return fmt.Errorf("写入成员角色：%w", err)
	}
	return nil
}

func revokeMemberSessions(ctx context.Context, tx pgx.Tx, targetID string) error {
	if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, targetID); err != nil {
		return fmt.Errorf("撤销成员会话：%w", err)
	}
	return nil
}

func insertMemberAudit(ctx context.Context, tx pgx.Tx, actorID, action, targetID string, details map[string]any) error {
	encodedDetails, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("编码成员审计详情：%w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_logs (actor_id,action,target_type,target_id,details)
		VALUES ($1,$2,'user',$3,$4)
	`, actorID, action, targetID, encodedDetails); err != nil {
		return fmt.Errorf("写入成员审计：%w", err)
	}
	return nil
}

func duplicateEmailError(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505" && postgresError.ConstraintName == "users_email_unique"
}

func containsRole(roles []RoleName, wanted RoleName) bool {
	for _, role := range roles {
		if role == wanted {
			return true
		}
	}
	return false
}

func roleStrings(roles []RoleName) []string {
	values := make([]string, len(roles))
	for index, role := range roles {
		values[index] = string(role)
	}
	return values
}

func permissionsForRole(role RoleName) ([]string, bool) {
	values := map[RoleName][]string{
		RoleAdmin:     {PermissionAdmin, PermissionExecute, PermissionPublishScript, PermissionRead},
		RoleOperator:  {PermissionExecute, PermissionRead},
		RoleDeveloper: {PermissionPublishScript, PermissionRead},
		RoleViewer:    {PermissionRead},
	}
	permissions, ok := values[role]
	return permissions, ok
}
