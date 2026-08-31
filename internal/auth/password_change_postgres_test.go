package auth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresPasswordChangeLimitsSixthAttempt(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	store := auth.NewPostgresPasswordChangeStore(db)
	now := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)

	for attempt := 1; attempt <= 5; attempt++ {
		allowed, err := store.RegisterAttempt(context.Background(), "user-1", "203.0.113.8", now)
		if err != nil {
			t.Fatalf("第 %d 次登记限速：%v", attempt, err)
		}
		if !allowed {
			t.Fatalf("第 %d 次请求应允许", attempt)
		}
	}

	allowed, err := store.RegisterAttempt(context.Background(), "user-1", "203.0.113.8", now)
	if err != nil {
		t.Fatalf("第六次登记限速：%v", err)
	}
	if allowed {
		t.Fatal("第六次请求必须被限速")
	}
}

func TestPostgresPasswordChangeLimitsSixthAttemptFromSharedIP(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	store := auth.NewPostgresPasswordChangeStore(db)
	now := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)

	for attempt := 1; attempt <= 5; attempt++ {
		allowed, err := store.RegisterAttempt(
			context.Background(),
			"user-"+string(rune('a'+attempt)),
			"203.0.113.8",
			now,
		)
		if err != nil || !allowed {
			t.Fatalf("共享 IP 第 %d 次请求应允许：allowed=%v err=%v", attempt, allowed, err)
		}
	}

	allowed, err := store.RegisterAttempt(context.Background(), "user-z", "203.0.113.8", now)
	if err != nil {
		t.Fatalf("共享 IP 第六次登记：%v", err)
	}
	if allowed {
		t.Fatal("共享 IP 的第六次请求必须被限速")
	}
}

func TestPostgresPasswordChangeResetsExpiredWindow(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	store := auth.NewPostgresPasswordChangeStore(db)
	windowStart := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)

	for attempt := 1; attempt <= 5; attempt++ {
		allowed, err := store.RegisterAttempt(context.Background(), "user-1", "203.0.113.8", windowStart)
		if err != nil || !allowed {
			t.Fatalf("窗口内第 %d 次请求应允许：allowed=%v err=%v", attempt, allowed, err)
		}
	}

	allowed, err := store.RegisterAttempt(
		context.Background(),
		"user-1",
		"203.0.113.8",
		windowStart.Add(15*time.Minute),
	)
	if err != nil || !allowed {
		t.Fatalf("15 分钟后必须重置限速窗口：allowed=%v err=%v", allowed, err)
	}
}

func TestPostgresPasswordChangeCommitsPasswordSessionsAndAuditAtomically(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	ctx := context.Background()
	oldHash := "old-password-hash"
	newHash := "new-password-hash"
	var userID string
	if err := db.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ('admin@example.com', '系统管理员', $1)
		RETURNING id::text
	`, oldHash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	currentHash := sha256.Sum256([]byte("current-token"))
	otherHash := sha256.Sum256([]byte("other-token"))
	now := time.Date(2026, 8, 31, 3, 15, 0, 0, time.UTC)
	if _, err := db.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at)
		VALUES
			('11111111-1111-4111-8111-111111111111', $1, $2, $4, $3),
			('22222222-2222-4222-8222-222222222222', $1, $5, $4, $3)
	`, userID, currentHash[:], now, now.Add(time.Hour), otherHash[:]); err != nil {
		t.Fatal(err)
	}

	store := auth.NewPostgresPasswordChangeStore(db)
	err := store.CommitPasswordChange(ctx, auth.PasswordChangeCommit{
		UserID:             userID,
		ExpectedHash:       oldHash,
		NewHash:            newHash,
		CurrentSessionHash: currentHash[:],
		IPAddress:          "203.0.113.8",
		ChangedAt:          now,
	})
	if err != nil {
		t.Fatalf("提交改密事务：%v", err)
	}

	var storedHash string
	if err := db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, userID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != newHash {
		t.Fatalf("密码哈希未更新：%q", storedHash)
	}

	rows, err := db.Query(ctx, `SELECT token_hash, revoked_at IS NOT NULL FROM sessions WHERE user_id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seenCurrent := false
	seenOther := false
	for rows.Next() {
		var tokenHash []byte
		var revoked bool
		if err := rows.Scan(&tokenHash, &revoked); err != nil {
			t.Fatal(err)
		}
		switch {
		case bytes.Equal(tokenHash, currentHash[:]):
			seenCurrent = true
			if revoked {
				t.Fatal("当前会话不得撤销")
			}
		case bytes.Equal(tokenHash, otherHash[:]):
			seenOther = true
			if !revoked {
				t.Fatal("其他会话必须撤销")
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !seenCurrent || !seenOther {
		t.Fatalf("会话断言不完整：current=%v other=%v", seenCurrent, seenOther)
	}

	var action, targetType, targetID, ipAddress, details string
	if err := db.QueryRow(ctx, `
		SELECT action, target_type, target_id, ip_address::text, details::text
		FROM audit_logs ORDER BY created_at DESC, id DESC LIMIT 1
	`).Scan(&action, &targetType, &targetID, &ipAddress, &details); err != nil {
		t.Fatal(err)
	}
	if action != "auth.password.change" || targetType != "user" || targetID != userID {
		t.Fatalf("审计目标错误：%s %s %s", action, targetType, targetID)
	}
	if ipAddress != "203.0.113.8/32" || details != "{}" {
		t.Fatalf("审计内容错误：ip=%q details=%q", ipAddress, details)
	}
}

func TestPostgresPasswordChangeRejectsStaleHashWithoutSideEffects(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000010_password_change_security.up.sql")
	ctx := context.Background()
	var userID string
	if err := db.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ('stale@example.com', '并发管理员', 'current-hash')
		RETURNING id::text
	`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	currentHash := sha256.Sum256([]byte("current-token"))
	otherHash := sha256.Sum256([]byte("other-token"))
	now := time.Date(2026, 8, 31, 3, 30, 0, 0, time.UTC)
	if _, err := db.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at)
		VALUES
			('33333333-3333-4333-8333-333333333333', $1, $2, $4, $3),
			('44444444-4444-4444-8444-444444444444', $1, $5, $4, $3)
	`, userID, currentHash[:], now, now.Add(time.Hour), otherHash[:]); err != nil {
		t.Fatal(err)
	}

	store := auth.NewPostgresPasswordChangeStore(db)
	err := store.CommitPasswordChange(ctx, auth.PasswordChangeCommit{
		UserID:             userID,
		ExpectedHash:       "stale-hash",
		NewHash:            "unexpected-new-hash",
		CurrentSessionHash: currentHash[:],
		IPAddress:          "203.0.113.8",
		ChangedAt:          now,
	})
	if !errors.Is(err, auth.ErrPasswordChanged) {
		t.Fatalf("旧哈希必须被拒绝：%v", err)
	}

	var storedHash string
	var revokedCount, auditCount int
	if err := db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, userID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NOT NULL`, userID).Scan(&revokedCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE actor_id=$1 AND action='auth.password.change'`, userID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if storedHash != "current-hash" || revokedCount != 0 || auditCount != 0 {
		t.Fatalf("并发拒绝必须整体回滚：hash=%q revoked=%d audits=%d", storedHash, revokedCount, auditCount)
	}
}
