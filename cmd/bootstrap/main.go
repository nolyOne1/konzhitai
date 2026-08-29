package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/store/postgres"
)

type config struct {
	DatabaseURL string
	Email       string
	DisplayName string
	Password    string
}

func main() {
	configuration, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, configuration.DatabaseURL)
	if err != nil {
		log.Fatalf("连接数据库失败：%v", err)
	}
	defer db.Close()
	if err := bootstrap(ctx, db, configuration); err != nil {
		log.Fatalf("初始化管理员失败：%v", err)
	}
	log.Printf("管理员 %s 已初始化；请立即清除初始化密码环境变量", configuration.Email)
}

func loadConfig(getenv func(string) string) (config, error) {
	result := config{
		DatabaseURL: strings.TrimSpace(getenv("YUNLING_DATABASE_URL")),
		Email:       strings.ToLower(strings.TrimSpace(getenv("YUNLING_BOOTSTRAP_EMAIL"))),
		DisplayName: strings.TrimSpace(getenv("YUNLING_BOOTSTRAP_NAME")),
		Password:    getenv("YUNLING_BOOTSTRAP_PASSWORD"),
	}
	if result.DatabaseURL == "" {
		return config{}, errors.New("未设置数据库地址 YUNLING_DATABASE_URL")
	}
	if result.Email == "" || !strings.Contains(result.Email, "@") {
		return config{}, errors.New("初始化管理员邮箱无效")
	}
	if result.DisplayName == "" {
		result.DisplayName = "系统管理员"
	}
	if len(result.Password) < 12 {
		return config{}, errors.New("初始化管理员密码至少需要 12 位")
	}
	return result, nil
}

func bootstrap(ctx context.Context, db *pgxpool.Pool, configuration config) error {
	passwordHash, err := auth.HashPassword(configuration.Password)
	if err != nil {
		return fmt.Errorf("生成管理员密码哈希：%w", err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开始初始化事务：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	roles := []struct {
		name        auth.RoleName
		permissions string
	}{
		{auth.RoleAdmin, `["system.admin","operations.execute","scripts.publish","system.read"]`},
		{auth.RoleOperator, `["operations.execute","system.read"]`},
		{auth.RoleDeveloper, `["scripts.publish","system.read"]`},
		{auth.RoleViewer, `["system.read"]`},
	}
	for _, role := range roles {
		if _, err := tx.Exec(ctx, `
			INSERT INTO roles (name, permissions) VALUES ($1,$2::jsonb)
			ON CONFLICT (name) DO UPDATE SET permissions=EXCLUDED.permissions
		`, role.name, role.permissions); err != nil {
			return fmt.Errorf("初始化角色 %s：%w", role.name, err)
		}
	}
	var userID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash, enabled)
		VALUES ($1,$2,$3,true)
		ON CONFLICT ((lower(email))) DO UPDATE
		SET display_name=EXCLUDED.display_name, password_hash=EXCLUDED.password_hash,
		    enabled=true, updated_at=now()
		RETURNING id
	`, configuration.Email, configuration.DisplayName, passwordHash).Scan(&userID); err != nil {
		return fmt.Errorf("创建或更新管理员：%w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE name='admin'
		ON CONFLICT (user_id, role_id) DO NOTHING
	`, userID); err != nil {
		return fmt.Errorf("授予管理员角色：%w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交初始化事务：%w", err)
	}
	return nil
}
