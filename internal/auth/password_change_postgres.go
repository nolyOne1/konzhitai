package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const passwordAttemptLimit = 5

var ErrPasswordChanged = errors.New("密码已被其他请求修改")

type PasswordChangeCommit struct {
	UserID             string
	ExpectedHash       string
	NewHash            string
	CurrentSessionHash []byte
	IPAddress          string
	ChangedAt          time.Time
}

type PasswordChangeStore interface {
	PasswordHash(context.Context, string) (string, error)
	RegisterAttempt(context.Context, string, string, time.Time) (bool, error)
	CommitPasswordChange(context.Context, PasswordChangeCommit) error
}

type PostgresPasswordChangeStore struct {
	db *pgxpool.Pool
}

func NewPostgresPasswordChangeStore(db *pgxpool.Pool) *PostgresPasswordChangeStore {
	return &PostgresPasswordChangeStore{db: db}
}

func (s *PostgresPasswordChangeStore) PasswordHash(ctx context.Context, userID string) (string, error) {
	if s == nil || s.db == nil || strings.TrimSpace(userID) == "" {
		return "", errors.New("改密仓储不可用")
	}
	var passwordHash string
	if err := s.db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, userID).Scan(&passwordHash); err != nil {
		return "", fmt.Errorf("读取用户密码哈希：%w", err)
	}
	return passwordHash, nil
}

func (s *PostgresPasswordChangeStore) RegisterAttempt(
	ctx context.Context,
	userID string,
	ipAddress string,
	now time.Time,
) (bool, error) {
	if s == nil || s.db == nil || strings.TrimSpace(userID) == "" || strings.TrimSpace(ipAddress) == "" {
		return false, errors.New("改密限速参数无效")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("开始改密限速事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	allowed := true
	for _, subject := range []struct {
		scope string
		value string
	}{
		{scope: "password_user", value: userID},
		{scope: "password_ip", value: ipAddress},
	} {
		subjectHash := sha256.Sum256([]byte(subject.value))
		var attempts int
		if err := tx.QueryRow(ctx, `
			INSERT INTO auth_rate_limits (scope, subject_hash, window_started_at, attempts)
			VALUES ($1, $2, $3, 1)
			ON CONFLICT (scope, subject_hash) DO UPDATE SET
				window_started_at = CASE
					WHEN auth_rate_limits.window_started_at <= EXCLUDED.window_started_at - interval '15 minutes'
					THEN EXCLUDED.window_started_at
					ELSE auth_rate_limits.window_started_at
				END,
				attempts = CASE
					WHEN auth_rate_limits.window_started_at <= EXCLUDED.window_started_at - interval '15 minutes'
					THEN 1
					ELSE auth_rate_limits.attempts + 1
				END
			RETURNING attempts
		`, subject.scope, subjectHash[:], now.UTC()).Scan(&attempts); err != nil {
			return false, fmt.Errorf("登记改密限速：%w", err)
		}
		if attempts > passwordAttemptLimit {
			allowed = false
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("提交改密限速事务：%w", err)
	}
	return allowed, nil
}

func (s *PostgresPasswordChangeStore) CommitPasswordChange(ctx context.Context, change PasswordChangeCommit) error {
	if s == nil || s.db == nil || strings.TrimSpace(change.UserID) == "" ||
		change.ExpectedHash == "" || change.NewHash == "" || len(change.CurrentSessionHash) != sha256.Size {
		return errors.New("改密提交参数无效")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始改密事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var storedHash string
	if err := tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1 FOR UPDATE`, change.UserID).Scan(&storedHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPasswordChanged
		}
		return fmt.Errorf("锁定改密用户：%w", err)
	}
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(change.ExpectedHash)) != 1 {
		return ErrPasswordChanged
	}
	changedAt := change.ChangedAt.UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE users SET password_hash=$2, updated_at=$3 WHERE id=$1
	`, change.UserID, change.NewHash, changedAt); err != nil {
		return fmt.Errorf("更新用户密码：%w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sessions
		SET revoked_at=$3
		WHERE user_id=$1 AND token_hash<>$2 AND revoked_at IS NULL
	`, change.UserID, change.CurrentSessionHash, changedAt); err != nil {
		return fmt.Errorf("撤销其他会话：%w", err)
	}
	userHash := sha256.Sum256([]byte(change.UserID))
	if _, err := tx.Exec(ctx, `
		DELETE FROM auth_rate_limits WHERE scope='password_user' AND subject_hash=$1
	`, userHash[:]); err != nil {
		return fmt.Errorf("清理改密用户限速：%w", err)
	}
	var ipAddress any
	if strings.TrimSpace(change.IPAddress) != "" {
		ipAddress = strings.TrimSpace(change.IPAddress)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_logs (
			id, actor_id, action, target_type, target_id, details, ip_address, created_at
		) VALUES (
			gen_random_uuid(), $1::uuid, 'auth.password.change', 'user',
			($1::uuid)::text, '{}'::jsonb, $2, $3
		)
	`, change.UserID, ipAddress, changedAt); err != nil {
		return fmt.Errorf("记录改密审计：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交改密事务：%w", err)
	}
	return nil
}

var _ PasswordChangeStore = (*PostgresPasswordChangeStore)(nil)
