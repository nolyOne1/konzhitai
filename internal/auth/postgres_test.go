package auth_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresRepositoryLoadsUserRolesAndSession(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	testpostgres.ApplyMigration(t, db, "000013_member_lifecycle.up.sql")
	ctx := context.Background()

	passwordHash, err := auth.HashPassword("正确密码")
	if err != nil {
		t.Fatalf("生成测试密码哈希：%v", err)
	}
	var userID string
	err = db.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ('ops@example.com', '值班运维', $1)
		RETURNING id
	`, passwordHash).Scan(&userID)
	if err != nil {
		t.Fatalf("插入测试用户：%v", err)
	}
	_, err = db.Exec(ctx, `
		WITH role AS (
			INSERT INTO roles (name, permissions)
			VALUES ('operator', '["operations.execute", "system.read"]')
			RETURNING id
		)
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM role
	`, userID)
	if err != nil {
		t.Fatalf("插入测试角色：%v", err)
	}

	repository := auth.NewPostgresRepository(db)
	user, err := repository.FindByEmail(ctx, "ops@example.com")
	if err != nil {
		t.Fatalf("按邮箱读取用户：%v", err)
	}
	if len(user.Roles) != 1 || user.Roles[0] != auth.RoleOperator {
		t.Fatalf("应读取用户的运维角色，实际为 %#v", user.Roles)
	}

	tokenHash := sha256.Sum256([]byte("test-token"))
	err = repository.Create(ctx, auth.StoredSession{
		ID:                   "123e4567-e89b-42d3-a456-426614174000",
		UserID:               userID,
		TokenHash:            tokenHash[:],
		ExpectedPasswordHash: passwordHash,
		ExpiresAt:            time.Now().Add(time.Hour),
		CreatedAt:            time.Now(),
	})
	if err != nil {
		t.Fatalf("保存服务端会话：%v", err)
	}
	principal, err := repository.FindPrincipal(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("读取有效会话：%v", err)
	}
	if principal.DisplayName != "值班运维" || len(principal.Roles) != 1 {
		t.Fatalf("会话应关联用户和角色，实际为 %+v", principal)
	}
}

func TestPostgresRepositoryLoadsMustChangePassword(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	testpostgres.ApplyMigration(t, db, "000013_member_lifecycle.up.sql")
	ctx := context.Background()
	var userID string
	if err := db.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash, must_change_password)
		VALUES ('temporary@example.com', '临时成员', 'hash', true)
		RETURNING id::text
	`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	repository := auth.NewPostgresRepository(db)

	user, err := repository.FindByEmail(ctx, "temporary@example.com")
	if err != nil {
		t.Fatalf("读取临时密码用户：%v", err)
	}
	if !user.MustChangePassword {
		t.Fatal("用户查询必须读取首次改密标记")
	}

	tokenHash := sha256.Sum256([]byte("temporary-session"))
	if err := repository.Create(ctx, auth.StoredSession{
		ID:                   "55555555-5555-4555-8555-555555555555",
		UserID:               userID,
		TokenHash:            tokenHash[:],
		ExpectedPasswordHash: "hash",
		ExpiresAt:            time.Now().Add(time.Hour),
		CreatedAt:            time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	principal, err := repository.FindPrincipal(ctx, tokenHash[:])
	if err != nil {
		t.Fatalf("读取临时密码会话：%v", err)
	}
	if !principal.MustChangePassword {
		t.Fatal("会话主体必须读取首次改密标记")
	}
}

func TestPostgresRepositoryRejectsRemovedUserLoginAndSession(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	testpostgres.ApplyMigration(t, db, "000013_member_lifecycle.up.sql")
	ctx := context.Background()
	var userID string
	if err := db.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash, enabled, removed_at)
		VALUES ('removed@example.com', '已移除成员', 'hash', true, now())
		RETURNING id::text
	`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	tokenHash := sha256.Sum256([]byte("removed-session"))
	if _, err := db.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at)
		VALUES ('66666666-6666-4666-8666-666666666666', $1, $2, now() + interval '1 hour', now())
	`, userID, tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	repository := auth.NewPostgresRepository(db)

	if _, err := repository.FindByEmail(ctx, "removed@example.com"); !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("已移除成员不得登录：%v", err)
	}
	if _, err := repository.FindPrincipal(ctx, tokenHash[:]); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("已移除成员的现存会话必须失效：%v", err)
	}
}
