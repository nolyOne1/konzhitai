package postgres_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/testpostgres"
)

func TestMemberLifecycleMigrationUpgradesExistingV12LoginAndSession(t *testing.T) {
	db := testpostgres.Start(t)
	root := testpostgres.RepositoryRoot(t)
	migrations, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		name := filepath.Base(migration)
		if strings.HasPrefix(name, "000013_") {
			continue
		}
		testpostgres.ApplyMigration(t, db, name)
	}
	ctx := context.Background()
	var version int
	if err := db.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 12 {
		t.Fatalf("升级前必须是真实 v12：version=%d err=%v", version, err)
	}

	passwordHash, err := auth.HashPassword("existing-password-2026")
	if err != nil {
		t.Fatal(err)
	}
	var userID, roleID, sessionID string
	if err := db.QueryRow(ctx, `
		INSERT INTO users (email,display_name,password_hash,enabled)
		VALUES ('existing@example.com','现有管理员',$1,true) RETURNING id::text
	`, passwordHash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO roles (name,permissions) VALUES ('admin','["admin"]') RETURNING id::text
	`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO user_roles (user_id,role_id) VALUES ($1,$2)`, userID, roleID); err != nil {
		t.Fatal(err)
	}
	tokenHash := []byte("existing-v12-session-token-hash")
	if err := db.QueryRow(ctx, `
		INSERT INTO sessions (user_id,token_hash,expires_at)
		VALUES ($1,$2,$3) RETURNING id::text
	`, userID, tokenHash, time.Now().Add(time.Hour)).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}

	testpostgres.ApplyMigration(t, db, "000013_member_lifecycle.up.sql")
	if err := db.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 13 {
		t.Fatalf("升级后 schema 版本错误：version=%d err=%v", version, err)
	}
	var mustChange bool
	var removedAt *time.Time
	if err := db.QueryRow(ctx, `SELECT must_change_password,removed_at FROM users WHERE id=$1`, userID).Scan(&mustChange, &removedAt); err != nil {
		t.Fatal(err)
	}
	if mustChange || removedAt != nil {
		t.Fatalf("v12 现有账号升级默认状态错误：mustChange=%v removedAt=%v", mustChange, removedAt)
	}
	var indexExists bool
	if err := db.QueryRow(ctx, `SELECT to_regclass('public.users_removed_at_idx') IS NOT NULL`).Scan(&indexExists); err != nil || !indexExists {
		t.Fatalf("v13 索引缺失：exists=%v err=%v", indexExists, err)
	}

	repository := auth.NewPostgresRepository(db)
	user, err := repository.FindByEmail(ctx, "existing@example.com")
	if err != nil {
		t.Fatalf("升级后现有账号不可登录读取：%v", err)
	}
	valid, err := auth.VerifyPassword(user.PasswordHash, "existing-password-2026")
	if err != nil || !valid || user.ID != userID || user.MustChangePassword {
		t.Fatalf("升级后现有登录凭据不兼容：user=%+v valid=%v err=%v", user, valid, err)
	}
	principal, err := repository.FindPrincipal(ctx, tokenHash)
	if err != nil {
		t.Fatalf("升级后 v12 活会话不可用：%v", err)
	}
	if principal.UserID != userID || principal.MustChangePassword || len(principal.Roles) != 1 || principal.Roles[0] != auth.RoleAdmin {
		t.Fatalf("升级后会话身份错误：%+v", principal)
	}
	var retainedSessionID string
	if err := db.QueryRow(ctx, `SELECT id::text FROM sessions WHERE token_hash=$1 AND revoked_at IS NULL`, tokenHash).Scan(&retainedSessionID); err != nil || retainedSessionID != sessionID {
		t.Fatalf("升级必须保留 v12 会话：got=%s want=%s err=%v", retainedSessionID, sessionID, err)
	}
}
