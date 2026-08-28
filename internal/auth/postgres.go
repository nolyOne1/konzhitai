package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	db *pgxpool.Pool
}

func NewPostgresRepository(db *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func (r *PostgresRepository) FindByEmail(ctx context.Context, email string) (User, error) {
	var user User
	var roles []string
	err := r.db.QueryRow(ctx, `
		SELECT
			u.id,
			u.email,
			u.display_name,
			u.password_hash,
			u.enabled,
			u.created_at,
			COALESCE(
				array_agg(ro.name ORDER BY ro.name) FILTER (WHERE ro.name IS NOT NULL),
				ARRAY[]::text[]
			)
		FROM users u
		LEFT JOIN user_roles ur ON ur.user_id = u.id
		LEFT JOIN roles ro ON ro.id = ur.role_id
		WHERE lower(u.email) = lower($1)
		GROUP BY u.id
	`, email).Scan(
		&user.ID,
		&user.Email,
		&user.DisplayName,
		&user.PasswordHash,
		&user.Enabled,
		&user.CreatedAt,
		&roles,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("读取用户：%w", err)
	}
	user.Roles = toRoleNames(roles)
	return user, nil
}

func (r *PostgresRepository) Create(ctx context.Context, session StoredSession) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, session.ID, session.UserID, session.TokenHash, session.ExpiresAt, session.CreatedAt)
	if err != nil {
		return fmt.Errorf("写入服务端会话：%w", err)
	}
	return nil
}

func (r *PostgresRepository) FindPrincipal(ctx context.Context, tokenHash []byte) (Principal, error) {
	var principal Principal
	var roles []string
	err := r.db.QueryRow(ctx, `
		SELECT
			u.id,
			u.email,
			u.display_name,
			COALESCE(
				array_agg(ro.name ORDER BY ro.name) FILTER (WHERE ro.name IS NOT NULL),
				ARRAY[]::text[]
			)
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		LEFT JOIN user_roles ur ON ur.user_id = u.id
		LEFT JOIN roles ro ON ro.id = ur.role_id
		WHERE s.token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.expires_at > now()
		  AND u.enabled = true
		GROUP BY u.id
	`, tokenHash).Scan(
		&principal.UserID,
		&principal.Email,
		&principal.DisplayName,
		&roles,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrSessionNotFound
	}
	if err != nil {
		return Principal{}, fmt.Errorf("读取服务端会话：%w", err)
	}
	principal.Roles = toRoleNames(roles)
	return principal, nil
}

func (r *PostgresRepository) Revoke(ctx context.Context, tokenHash []byte) error {
	_, err := r.db.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, tokenHash)
	if err != nil {
		return fmt.Errorf("撤销服务端会话：%w", err)
	}
	return nil
}

func toRoleNames(values []string) []RoleName {
	roles := make([]RoleName, 0, len(values))
	for _, value := range values {
		roles = append(roles, RoleName(value))
	}
	return roles
}

var _ UserRepository = (*PostgresRepository)(nil)
var _ SessionRepository = (*PostgresRepository)(nil)
