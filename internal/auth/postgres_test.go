package auth_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresRepositoryLoadsUserRolesAndSession(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
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
		ID:        "123e4567-e89b-42d3-a456-426614174000",
		UserID:    userID,
		TokenHash: tokenHash[:],
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Now(),
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

func TestPostgresRepositoryReplacesAndListsMemberRoles(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	ctx := context.Background()
	passwordHash, err := auth.HashPassword("团队成员密码")
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := db.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ('developer@example.com', '脚本开发者', $1)
		RETURNING id
	`, passwordHash).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	service := auth.NewTeamService(auth.NewPostgresRepository(db))
	updated, err := service.UpdateRoles(ctx, userID, []auth.RoleName{auth.RoleViewer, auth.RoleDeveloper})
	if err != nil {
		t.Fatalf("替换成员角色：%v", err)
	}
	if len(updated.Roles) != 2 || updated.Roles[0] != auth.RoleDeveloper || updated.Roles[1] != auth.RoleViewer {
		t.Fatalf("返回角色不正确：%v", updated.Roles)
	}
	members, err := service.List(ctx)
	if err != nil {
		t.Fatalf("读取团队成员：%v", err)
	}
	if len(members) != 1 || members[0].ID != userID || len(members[0].Roles) != 2 {
		t.Fatalf("成员列表未持久化角色：%+v", members)
	}
}
