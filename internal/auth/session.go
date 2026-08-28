package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidCredentials = errors.New("账号或密码错误")
	ErrUserNotFound       = errors.New("用户不存在")
	ErrSessionNotFound    = errors.New("会话不存在或已失效")
)

type UserRepository interface {
	FindByEmail(ctx context.Context, email string) (User, error)
}

type SessionRepository interface {
	Create(ctx context.Context, session StoredSession) error
	FindPrincipal(ctx context.Context, tokenHash []byte) (Principal, error)
	Revoke(ctx context.Context, tokenHash []byte) error
}

type Service struct {
	users      UserRepository
	sessions   SessionRepository
	sessionTTL time.Duration
	now        func() time.Time
}

func NewService(users UserRepository, sessions SessionRepository) *Service {
	return &Service{
		users:      users,
		sessions:   sessions,
		sessionTTL: 24 * time.Hour,
		now:        time.Now,
	}
}

func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	user, err := s.users.FindByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil || !user.Enabled {
		return Session{}, ErrInvalidCredentials
	}
	valid, err := VerifyPassword(user.PasswordHash, password)
	if err != nil || !valid {
		return Session{}, ErrInvalidCredentials
	}

	token, err := randomToken(32)
	if err != nil {
		return Session{}, err
	}
	id, err := randomUUID()
	if err != nil {
		return Session{}, err
	}
	now := s.now().UTC()
	stored := StoredSession{
		ID:        id,
		UserID:    user.ID,
		TokenHash: tokenHash(token),
		ExpiresAt: now.Add(s.sessionTTL),
		CreatedAt: now,
	}
	if err := s.sessions.Create(ctx, stored); err != nil {
		return Session{}, fmt.Errorf("保存会话：%w", err)
	}
	return Session{
		ID:        stored.ID,
		UserID:    stored.UserID,
		Token:     token,
		Roles:     append([]RoleName(nil), user.Roles...),
		ExpiresAt: stored.ExpiresAt,
		CreatedAt: stored.CreatedAt,
	}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrSessionNotFound
	}
	principal, err := s.sessions.FindPrincipal(ctx, tokenHash(token))
	if err != nil {
		return Principal{}, ErrSessionNotFound
	}
	return principal, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.sessions.Revoke(ctx, tokenHash(token))
}

func tokenHash(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func randomToken(length int) (string, error) {
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成会话令牌：%w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成会话编号：%w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
