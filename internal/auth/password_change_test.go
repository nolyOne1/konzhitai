package auth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
)

type fakePasswordChangeStore struct {
	passwordHash string
	allowed      bool
	commit       *auth.PasswordChangeCommit
}

func (s *fakePasswordChangeStore) PasswordHash(context.Context, string) (string, error) {
	return s.passwordHash, nil
}

func (s *fakePasswordChangeStore) RegisterAttempt(context.Context, string, string, time.Time) (bool, error) {
	return s.allowed, nil
}

func (s *fakePasswordChangeStore) CommitPasswordChange(_ context.Context, change auth.PasswordChangeCommit) error {
	copyOfChange := change
	copyOfChange.CurrentSessionHash = append([]byte(nil), change.CurrentSessionHash...)
	s.commit = &copyOfChange
	return nil
}

func TestPasswordChangeServiceRejectsInvalidCredentialsAndPolicyUniformly(t *testing.T) {
	currentHash, err := auth.HashPassword("correct-current-password")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name            string
		currentPassword string
		newPassword     string
	}{
		{name: "错误当前密码", currentPassword: "wrong-current-password", newPassword: "new-password-2026"},
		{name: "新密码少于十二位", currentPassword: "correct-current-password", newPassword: "short"},
		{name: "新密码与当前密码相同", currentPassword: "correct-current-password", newPassword: "correct-current-password"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakePasswordChangeStore{passwordHash: currentHash, allowed: true}
			service := auth.NewPasswordChangeService(store, func() time.Time {
				return time.Date(2026, 8, 31, 4, 0, 0, 0, time.UTC)
			})
			err := service.Change(
				context.Background(),
				auth.Principal{UserID: "user-1"},
				"current-token",
				test.currentPassword,
				test.newPassword,
				"203.0.113.8",
			)
			if !errors.Is(err, auth.ErrPasswordRejected) {
				t.Fatalf("必须返回统一拒绝错误：%v", err)
			}
			if store.commit != nil {
				t.Fatal("验证失败不得提交改密事务")
			}
		})
	}
}

func TestPasswordChangeServiceRejectsRateLimitedRequest(t *testing.T) {
	store := &fakePasswordChangeStore{allowed: false}
	service := auth.NewPasswordChangeService(store, time.Now)
	err := service.Change(
		context.Background(),
		auth.Principal{UserID: "user-1"},
		"current-token",
		"correct-current-password",
		"new-password-2026",
		"203.0.113.8",
	)
	if !errors.Is(err, auth.ErrPasswordRateLimited) {
		t.Fatalf("限速必须返回专用错误：%v", err)
	}
	if store.commit != nil {
		t.Fatal("限速请求不得提交改密事务")
	}
}

func TestPasswordChangeServiceCommitsNewHashAndCurrentSession(t *testing.T) {
	currentHash, err := auth.HashPassword("correct-current-password")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 4, 15, 0, 0, time.UTC)
	store := &fakePasswordChangeStore{passwordHash: currentHash, allowed: true}
	service := auth.NewPasswordChangeService(store, func() time.Time { return now })
	err = service.Change(
		context.Background(),
		auth.Principal{UserID: "user-1"},
		"current-token",
		"correct-current-password",
		"new-password-2026",
		"203.0.113.8",
	)
	if err != nil {
		t.Fatalf("修改密码：%v", err)
	}
	if store.commit == nil {
		t.Fatal("必须提交改密事务")
	}
	wantTokenHash := sha256.Sum256([]byte("current-token"))
	if store.commit.UserID != "user-1" || store.commit.ExpectedHash != currentHash ||
		!bytes.Equal(store.commit.CurrentSessionHash, wantTokenHash[:]) ||
		store.commit.IPAddress != "203.0.113.8" || !store.commit.ChangedAt.Equal(now) {
		t.Fatalf("提交内容错误：%+v", store.commit)
	}
	valid, err := auth.VerifyPassword(store.commit.NewHash, "new-password-2026")
	if err != nil || !valid {
		t.Fatalf("新密码哈希无效：valid=%v err=%v", valid, err)
	}
}
