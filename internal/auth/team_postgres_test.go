package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/testpostgres"
)

const nonexistentActorID = "00000000-0000-0000-0000-000000000000"

func TestPostgresMemberListFiltersLifecycleStates(t *testing.T) {
	db, _, targetID := memberDatabase(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `UPDATE users SET enabled=false WHERE id=$1`, targetID); err != nil {
		t.Fatal(err)
	}
	var removedID string
	if err := db.QueryRow(ctx, `
		INSERT INTO users (email,display_name,password_hash,enabled,removed_at)
		VALUES ('removed@example.com','已移除成员','hash',false,now()) RETURNING id::text
	`).Scan(&removedID); err != nil {
		t.Fatal(err)
	}

	repository := auth.NewPostgresRepository(db)
	tests := []struct {
		name   string
		status auth.MemberStatus
		want   []string
	}{
		{name: "active", status: auth.MemberStatusActive, want: []string{"admin-2@example.com", "admin@example.com"}},
		{name: "disabled", status: auth.MemberStatusDisabled, want: []string{"viewer@example.com"}},
		{name: "removed", status: auth.MemberStatusRemoved, want: []string{"removed@example.com"}},
		{name: "all excludes removed", status: auth.MemberStatusAll, want: []string{"admin-2@example.com", "admin@example.com", "viewer@example.com"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			members, err := repository.ListMembers(ctx, test.status)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(members))
			for index, member := range members {
				got[index] = member.Email
				if member.ID == removedID && member.RemovedAt == nil {
					t.Fatal("已移除成员必须返回 removedAt")
				}
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("状态 %q 筛选错误：got=%v want=%v", test.status, got, test.want)
			}
		})
	}
}

func TestPostgresMemberCreateMapsDuplicateEmailAndAudits(t *testing.T) {
	db, actorID, _ := memberDatabase(t)
	repository := auth.NewPostgresRepository(db)
	ctx := context.Background()
	hash, err := auth.HashPassword("temporary-password-2026")
	if err != nil {
		t.Fatal(err)
	}
	record := auth.CreateMemberRecord{
		Email: "new@example.com", DisplayName: "新成员", PasswordHash: hash,
		Roles: []auth.RoleName{auth.RoleDeveloper, auth.RoleViewer},
	}
	member, err := repository.CreateMember(ctx, actorID, record)
	if err != nil {
		t.Fatal(err)
	}
	if member.Email != record.Email || member.DisplayName != record.DisplayName || !member.Enabled || !member.MustChangePassword || member.RemovedAt != nil || !reflect.DeepEqual(member.Roles, record.Roles) {
		t.Fatalf("创建成员投影错误：%+v", member)
	}
	assertMemberAudit(t, db, actorID, member.ID, "member.create", hash, "temporary-password-2026")

	_, err = repository.CreateMember(ctx, actorID, auth.CreateMemberRecord{
		Email: "NEW@example.com", DisplayName: "重复成员", PasswordHash: hash, Roles: []auth.RoleName{auth.RoleViewer},
	})
	if !errors.Is(err, auth.ErrDuplicateEmail) {
		t.Fatalf("重复邮箱应映射 ErrDuplicateEmail：%v", err)
	}
}

func TestPostgresMemberReplaceRolesProtectsLastAdminAndAudits(t *testing.T) {
	db, actorID, targetID := memberDatabase(t)
	repository := auth.NewPostgresRepository(db)
	member, err := repository.ReplaceMemberRoles(context.Background(), actorID, targetID, []auth.RoleName{auth.RoleDeveloper, auth.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	want := []auth.RoleName{auth.RoleDeveloper, auth.RoleViewer}
	if !reflect.DeepEqual(member.Roles, want) {
		t.Fatalf("角色未被完整替换：got=%v want=%v", member.Roles, want)
	}
	assertMemberAudit(t, db, actorID, targetID, "member.roles.update", "password", "hash")

	singleDB, onlyAdminID := singleAdminDatabase(t)
	_, err = auth.NewPostgresRepository(singleDB).ReplaceMemberRoles(context.Background(), nonexistentActorID, onlyAdminID, []auth.RoleName{auth.RoleViewer})
	if !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("最后管理员不应被移除管理员角色：%v", err)
	}
}

func TestPostgresMemberDisableRevokesSessionsAndAuditsAtomically(t *testing.T) {
	db, actorID, targetID := memberDatabase(t)
	repository := auth.NewPostgresRepository(db)
	member, err := repository.SetMemberEnabled(context.Background(), actorID, targetID, false)
	if err != nil {
		t.Fatal(err)
	}
	if member.Enabled {
		t.Fatal("成员仍处于启用状态")
	}

	var liveSessions, audits int
	_ = db.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, targetID).Scan(&liveSessions)
	_ = db.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE actor_id=$1 AND target_id=$2 AND action='member.disable'`, actorID, targetID).Scan(&audits)
	if liveSessions != 0 || audits != 1 {
		t.Fatalf("事务结果错误：sessions=%d audits=%d", liveSessions, audits)
	}
	assertMemberAudit(t, db, actorID, targetID, "member.disable", "password", "hash")
	if _, err := repository.SetMemberEnabled(context.Background(), actorID, targetID, false); !errors.Is(err, auth.ErrMemberStateConflict) {
		t.Fatalf("重复停用应报状态冲突：%v", err)
	}
	member, err = repository.SetMemberEnabled(context.Background(), actorID, targetID, true)
	if err != nil || !member.Enabled {
		t.Fatalf("重新启用成员失败：member=%+v err=%v", member, err)
	}
	assertMemberAudit(t, db, actorID, targetID, "member.enable", "password", "hash")
}

func TestPostgresMemberMutationRollsBackWhenAuditFails(t *testing.T) {
	db, _, targetID := memberDatabase(t)
	repository := auth.NewPostgresRepository(db)
	_, err := repository.SetMemberEnabled(context.Background(), "00000000-0000-0000-0000-000000000000", targetID, false)
	if err == nil {
		t.Fatal("无效审计操作者应使事务失败")
	}
	var enabled bool
	var liveSessions int
	if err := db.QueryRow(context.Background(), `SELECT enabled FROM users WHERE id=$1`, targetID).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, targetID).Scan(&liveSessions); err != nil {
		t.Fatal(err)
	}
	if !enabled || liveSessions != 1 {
		t.Fatalf("审计失败后必须回滚状态和会话：enabled=%v sessions=%d", enabled, liveSessions)
	}
}

func TestPostgresMemberRemoveRestoreStaysDisabledAndAudits(t *testing.T) {
	db, actorID, targetID := memberDatabase(t)
	repository := auth.NewPostgresRepository(db)
	ctx := context.Background()
	removed, err := repository.RemoveMember(ctx, actorID, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if removed.RemovedAt == nil || removed.Enabled {
		t.Fatalf("移除应设置 removedAt 并停用：%+v", removed)
	}
	assertMemberAudit(t, db, actorID, targetID, "member.remove", "password", "hash")
	if _, err := repository.RemoveMember(ctx, actorID, targetID); !errors.Is(err, auth.ErrMemberStateConflict) {
		t.Fatalf("重复移除应报状态冲突：%v", err)
	}
	if _, err := repository.SetMemberEnabled(ctx, actorID, targetID, true); !errors.Is(err, auth.ErrMemberStateConflict) {
		t.Fatalf("已移除成员不能启用：%v", err)
	}

	restored, err := repository.RestoreMember(ctx, actorID, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.RemovedAt != nil || restored.Enabled {
		t.Fatalf("恢复成员必须仍保持停用：%+v", restored)
	}
	assertMemberAudit(t, db, actorID, targetID, "member.restore", "password", "hash")
	if _, err := repository.RestoreMember(ctx, actorID, targetID); !errors.Is(err, auth.ErrMemberStateConflict) {
		t.Fatalf("重复恢复应报状态冲突：%v", err)
	}
}

func TestPostgresMemberResetPasswordRevokesSessionsAndAudits(t *testing.T) {
	db, actorID, targetID := memberDatabase(t)
	repository := auth.NewPostgresRepository(db)
	newHash, err := auth.HashPassword("new-temporary-password-2026")
	if err != nil {
		t.Fatal(err)
	}
	member, err := repository.ResetMemberPassword(context.Background(), actorID, targetID, newHash)
	if err != nil {
		t.Fatal(err)
	}
	if !member.MustChangePassword {
		t.Fatal("重置密码后必须要求首次改密")
	}
	var storedHash string
	var liveSessions int
	if err := db.QueryRow(context.Background(), `SELECT password_hash FROM users WHERE id=$1`, targetID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != newHash {
		t.Fatal("新密码哈希未持久化")
	}
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, targetID).Scan(&liveSessions); err != nil {
		t.Fatal(err)
	}
	if liveSessions != 0 {
		t.Fatalf("重置密码应撤销全部会话：%d", liveSessions)
	}
	assertMemberAudit(t, db, actorID, targetID, "member.password.reset", newHash, "new-temporary-password-2026")
}

func TestPostgresMemberMutationPreservesLastAdmin(t *testing.T) {
	tests := []struct {
		name string
		call func(*auth.PostgresRepository, string) error
	}{
		{name: "disable", call: func(repository *auth.PostgresRepository, id string) error {
			_, err := repository.SetMemberEnabled(context.Background(), nonexistentActorID, id, false)
			return err
		}},
		{name: "remove", call: func(repository *auth.PostgresRepository, id string) error {
			_, err := repository.RemoveMember(context.Background(), nonexistentActorID, id)
			return err
		}},
		{name: "replace roles", call: func(repository *auth.PostgresRepository, id string) error {
			_, err := repository.ReplaceMemberRoles(context.Background(), nonexistentActorID, id, []auth.RoleName{auth.RoleViewer})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, actorID := singleAdminDatabase(t)
			err := test.call(auth.NewPostgresRepository(db), actorID)
			if !errors.Is(err, auth.ErrLastAdmin) {
				t.Fatalf("最后管理员应被保护：%v", err)
			}
		})
	}
}

func TestPostgresTeamServiceRejectsEquivalentUUIDSelfMutation(t *testing.T) {
	db, actorID, _ := memberDatabase(t)
	service := auth.NewTeamService(auth.NewPostgresRepository(db))

	if _, err := service.SetEnabled(context.Background(), actorID, strings.ToUpper(actorID), false); !errors.Is(err, auth.ErrCannotModifySelf) {
		t.Fatalf("服务到 PostgreSQL 必须拒绝等价 UUID 自停用：%v", err)
	}
	var enabled bool
	if err := db.QueryRow(context.Background(), `SELECT enabled FROM users WHERE id=$1`, actorID).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("等价 UUID 绕过后修改了当前管理员")
	}
}

func TestPostgresMemberCrossAdminMutationsSerializeWithoutDeadlock(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, *auth.PostgresRepository, string, string) error
	}{
		{
			name: "交叉降权",
			call: func(ctx context.Context, repository *auth.PostgresRepository, actorID, targetID string) error {
				_, err := repository.ReplaceMemberRoles(ctx, actorID, targetID, []auth.RoleName{auth.RoleViewer})
				return err
			},
		},
		{
			name: "交叉停用",
			call: func(ctx context.Context, repository *auth.PostgresRepository, actorID, targetID string) error {
				_, err := repository.SetMemberEnabled(ctx, actorID, targetID, false)
				return err
			},
		},
		{
			name: "交叉移除",
			call: func(ctx context.Context, repository *auth.PostgresRepository, actorID, targetID string) error {
				_, err := repository.RemoveMember(ctx, actorID, targetID)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, firstAdminID, _ := memberDatabase(t)
			var secondAdminID string
			if err := db.QueryRow(context.Background(), `SELECT id::text FROM users WHERE email='admin-2@example.com'`).Scan(&secondAdminID); err != nil {
				t.Fatal(err)
			}
			blocker, err := db.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Release()
			if _, err := blocker.Exec(context.Background(), `SELECT pg_advisory_lock(hashtext('yunling-member-admin'))`); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			repository := auth.NewPostgresRepository(db)
			results := make(chan error, 2)
			go func() { results <- test.call(ctx, repository, firstAdminID, secondAdminID) }()
			go func() { results <- test.call(ctx, repository, secondAdminID, firstAdminID) }()

			waitForAdvisoryWaiters(t, db, 2)
			if _, err := blocker.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('yunling-member-admin'))`); err != nil {
				t.Fatal(err)
			}

			succeeded := 0
			lastAdmin := 0
			for range 2 {
				err := <-results
				switch {
				case err == nil:
					succeeded++
				case errors.Is(err, auth.ErrLastAdmin):
					lastAdmin++
				default:
					t.Fatalf("交叉操作出现死锁或未知错误：%v", err)
				}
			}
			if succeeded != 1 || lastAdmin != 1 {
				t.Fatalf("交叉操作结果错误：success=%d lastAdmin=%d", succeeded, lastAdmin)
			}
			var effectiveAdmins int
			if err := db.QueryRow(context.Background(), `
				SELECT count(DISTINCT u.id)
				FROM users u
				JOIN user_roles ur ON ur.user_id=u.id
				JOIN roles r ON r.id=ur.role_id
				WHERE u.enabled=true AND u.removed_at IS NULL AND r.name='admin'
			`).Scan(&effectiveAdmins); err != nil {
				t.Fatal(err)
			}
			if effectiveAdmins != 1 {
				t.Fatalf("交叉操作后必须保留一名有效管理员：%d", effectiveAdmins)
			}
		})
	}
}

func waitForAdvisoryWaiters(t *testing.T, db *pgxpool.Pool, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := db.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 advisory lock 的事务未达到 %d 个", want)
}

func memberDatabase(t *testing.T) (*pgxpool.Pool, string, string) {
	t.Helper()
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	testpostgres.ApplyMigration(t, db, "000013_member_lifecycle.up.sql")
	ctx := context.Background()
	hash, err := auth.HashPassword("member-password-2026")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO roles (name, permissions) VALUES ('admin','[]'),('viewer','[]') ON CONFLICT (name) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	var actorID, secondAdminID, targetID string
	if err := db.QueryRow(ctx, `INSERT INTO users (email,display_name,password_hash) VALUES ('admin@example.com','管理员',$1) RETURNING id::text`, hash).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO users (email,display_name,password_hash) VALUES ('admin-2@example.com','备用管理员',$1) RETURNING id::text`, hash).Scan(&secondAdminID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO users (email,display_name,password_hash) VALUES ('viewer@example.com','只读成员',$1) RETURNING id::text`, hash).Scan(&targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO user_roles (user_id,role_id)
		SELECT user_id,id FROM roles CROSS JOIN (VALUES ($1::uuid),($2::uuid)) admins(user_id) WHERE name='admin'
		UNION ALL SELECT $3,id FROM roles WHERE name='viewer'
	`, actorID, secondAdminID, targetID); err != nil {
		t.Fatal(err)
	}
	sessionHash := sha256.Sum256([]byte("target-session"))
	if _, err := db.Exec(ctx, `INSERT INTO sessions (user_id,token_hash,expires_at) VALUES ($1,$2,now()+interval '1 hour')`, targetID, sessionHash[:]); err != nil {
		t.Fatal(err)
	}
	return db, actorID, targetID
}

func singleAdminDatabase(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	db, actorID, _ := memberDatabase(t)
	if _, err := db.Exec(context.Background(), `DELETE FROM users WHERE id<>$1`, actorID); err != nil {
		t.Fatal(err)
	}
	return db, actorID
}

func assertMemberAudit(t *testing.T, db *pgxpool.Pool, actorID, targetID, action string, forbidden ...string) {
	t.Helper()
	var targetType string
	var details []byte
	if err := db.QueryRow(context.Background(), `
		SELECT target_type,details FROM audit_logs
		WHERE actor_id=$1 AND target_id=$2 AND action=$3
		ORDER BY created_at DESC LIMIT 1
	`, actorID, targetID, action).Scan(&targetType, &details); err != nil {
		t.Fatalf("读取审计 %s：%v", action, err)
	}
	if targetType != "user" || !json.Valid(details) {
		t.Fatalf("审计投影错误：targetType=%q details=%s", targetType, details)
	}
	lowerDetails := strings.ToLower(string(details))
	for _, value := range append(forbidden, "password_hash", "temporarypassword", "plaintext") {
		if value != "" && strings.Contains(lowerDetails, strings.ToLower(value)) {
			t.Fatalf("审计 %s 泄露密码数据 %q：%s", action, value, details)
		}
	}
}
