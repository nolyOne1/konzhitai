package main

import (
	"context"
	"testing"

	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/testpostgres"
)

func TestLoadConfigRequiresDeploymentCredentials(t *testing.T) {
	values := map[string]string{
		"YUNLING_DATABASE_URL":       "postgres://yunling@postgres/yunling",
		"YUNLING_BOOTSTRAP_EMAIL":    "admin@example.com",
		"YUNLING_BOOTSTRAP_NAME":     "系统管理员",
		"YUNLING_BOOTSTRAP_PASSWORD": "long-test-password",
	}
	configuration, err := loadConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("完整配置应可加载：%v", err)
	}
	if configuration.Email != "admin@example.com" || configuration.DisplayName != "系统管理员" {
		t.Fatalf("管理员配置解析错误：%+v", configuration)
	}
	delete(values, "YUNLING_BOOTSTRAP_PASSWORD")
	if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("缺少初始化密码必须拒绝启动")
	}
}

func TestLoadConfigRejectsShortBootstrapPassword(t *testing.T) {
	values := map[string]string{
		"YUNLING_DATABASE_URL":       "postgres://yunling@postgres/yunling",
		"YUNLING_BOOTSTRAP_EMAIL":    "admin@example.com",
		"YUNLING_BOOTSTRAP_PASSWORD": "short",
	}
	if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("初始化管理员密码少于 12 位必须拒绝")
	}
}

func TestBootstrapCreatesAdministratorAndCanResetPassword(t *testing.T) {
	db := testpostgres.Start(t)
	for _, migration := range []string{
		"000001_initial.up.sql", "000002_agent_enrollment.up.sql", "000003_server_management.up.sql",
		"000004_script_sync_states.up.sql", "000005_task_scheduling.up.sql", "000006_scheduler_resources.up.sql",
		"000007_run_observability.up.sql", "000008_security_audit_alerts.up.sql",
	} {
		testpostgres.ApplyMigration(t, db, migration)
	}
	configuration := config{
		DatabaseURL: "unused", Email: "admin@example.com", DisplayName: "平台管理员", Password: "first-password-2026",
	}
	if err := bootstrap(context.Background(), db, configuration); err != nil {
		t.Fatalf("首次初始化管理员：%v", err)
	}
	configuration.Password = "second-password-2026"
	if err := bootstrap(context.Background(), db, configuration); err != nil {
		t.Fatalf("重复执行应重置管理员密码：%v", err)
	}
	var passwordHash string
	var adminRoles int
	if err := db.QueryRow(context.Background(), `SELECT password_hash FROM users WHERE lower(email)=lower($1)`, configuration.Email).Scan(&passwordHash); err != nil {
		t.Fatalf("读取初始化管理员：%v", err)
	}
	if err := db.QueryRow(context.Background(), `
		SELECT count(*) FROM user_roles AS member_role
		JOIN users AS member ON member.id=member_role.user_id
		JOIN roles AS role ON role.id=member_role.role_id
		WHERE lower(member.email)=lower($1) AND role.name='admin'
	`, configuration.Email).Scan(&adminRoles); err != nil {
		t.Fatalf("读取管理员角色：%v", err)
	}
	valid, err := auth.VerifyPassword(passwordHash, configuration.Password)
	if err != nil || !valid || adminRoles != 1 {
		t.Fatalf("初始化结果错误：passwordValid=%v roles=%d err=%v", valid, adminRoles, err)
	}
}
