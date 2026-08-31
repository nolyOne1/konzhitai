package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

var (
	ErrPasswordRejected    = errors.New("当前密码或新密码不符合要求")
	ErrPasswordRateLimited = errors.New("操作过于频繁，请稍后再试")
)

type PasswordChangeService struct {
	store PasswordChangeStore
	now   func() time.Time
}

func NewPasswordChangeService(store PasswordChangeStore, now func() time.Time) *PasswordChangeService {
	if now == nil {
		now = time.Now
	}
	return &PasswordChangeService{store: store, now: now}
}

func (s *PasswordChangeService) Change(
	ctx context.Context,
	principal Principal,
	sessionToken string,
	currentPassword string,
	newPassword string,
	ipAddress string,
) error {
	if s == nil || s.store == nil {
		return errors.New("改密服务不可用")
	}
	now := s.now().UTC()
	allowed, err := s.store.RegisterAttempt(ctx, principal.UserID, ipAddress, now)
	if err != nil {
		return fmt.Errorf("登记改密限速：%w", err)
	}
	if !allowed {
		return ErrPasswordRateLimited
	}
	passwordHash, err := s.store.PasswordHash(ctx, principal.UserID)
	if err != nil {
		return fmt.Errorf("读取改密用户：%w", err)
	}
	valid, err := VerifyPassword(passwordHash, currentPassword)
	if err != nil || !valid {
		return ErrPasswordRejected
	}
	if utf8.RuneCountInString(newPassword) < 12 || newPassword == currentPassword {
		return ErrPasswordRejected
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("生成新密码哈希：%w", err)
	}
	if err := s.store.CommitPasswordChange(ctx, PasswordChangeCommit{
		UserID:             principal.UserID,
		ExpectedHash:       passwordHash,
		NewHash:            newHash,
		CurrentSessionHash: tokenHash(sessionToken),
		IPAddress:          ipAddress,
		ChangedAt:          now,
	}); err != nil {
		return fmt.Errorf("提交改密事务：%w", err)
	}
	return nil
}
