package server_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/server"
	"yunling.local/platform/internal/testpostgres"
)

func TestEnrollmentTokenIsHashedAndCanOnlyBeUsedOnce(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000002_agent_enrollment.up.sql")
	testpostgres.ApplyMigration(t, db, "000003_server_management.up.sql")
	testpostgres.ApplyMigration(t, db, "000004_script_sync_states.up.sql")
	testpostgres.ApplyMigration(t, db, "000005_task_scheduling.up.sql")
	testpostgres.ApplyMigration(t, db, "000006_scheduler_resources.up.sql")
	ctx := context.Background()
	now := time.Date(2026, 8, 28, 11, 0, 0, 0, time.UTC)
	repository := server.NewPostgresRepository(db)
	service := server.NewEnrollmentService(repository, func() time.Time { return now })

	issued, err := service.CreateToken(ctx, server.EnrollmentTokenInput{
		Name:          "京东云执行节点-1",
		CloudProvider: "京东云",
		Region:        "华北",
		Labels:        map[string]string{"用途": "批处理"},
	})
	if err != nil {
		t.Fatalf("创建一次性注册令牌：%v", err)
	}
	if issued.Token == "" {
		t.Fatal("创建后必须返回仅显示一次的明文令牌")
	}
	var storedTokenHash []byte
	if err := db.QueryRow(ctx, `SELECT token_hash FROM server_enrollment_tokens WHERE id = $1`, issued.ID).Scan(&storedTokenHash); err != nil {
		t.Fatalf("读取已保存的令牌哈希：%v", err)
	}
	wantTokenHash := sha256.Sum256([]byte(issued.Token))
	if string(storedTokenHash) != string(wantTokenHash[:]) {
		t.Fatal("数据库必须保存注册令牌的 SHA-256 哈希")
	}
	if string(storedTokenHash) == issued.Token {
		t.Fatal("数据库不得保存明文注册令牌")
	}

	credentials, err := service.Enroll(ctx, issued.Token)
	if err != nil {
		t.Fatalf("首次使用注册令牌：%v", err)
	}
	if credentials.ServerID == "" || credentials.Credential == "" {
		t.Fatalf("首次注册必须返回服务器 ID 和独立凭据：%+v", credentials)
	}
	if _, err := service.Enroll(ctx, issued.Token); !errors.Is(err, server.ErrEnrollmentTokenInvalid) {
		t.Fatalf("注册令牌二次使用应失败，实际为 %v", err)
	}

	serverID, err := service.Authenticate(ctx, credentials.Credential)
	if err != nil {
		t.Fatalf("验证代理独立凭据：%v", err)
	}
	if serverID != credentials.ServerID {
		t.Fatalf("凭据应绑定服务器 %s，实际绑定 %s", credentials.ServerID, serverID)
	}
}

func TestPostgresHeartbeatAcceptsOnlyIncreasingSequence(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000002_agent_enrollment.up.sql")
	testpostgres.ApplyMigration(t, db, "000003_server_management.up.sql")
	testpostgres.ApplyMigration(t, db, "000004_script_sync_states.up.sql")
	testpostgres.ApplyMigration(t, db, "000005_task_scheduling.up.sql")
	testpostgres.ApplyMigration(t, db, "000006_scheduler_resources.up.sql")
	ctx := context.Background()
	serverID := "123e4567-e89b-42d3-a456-426614174100"
	_, err := db.Exec(ctx, `
		INSERT INTO servers (id, name, status)
		VALUES ($1, '心跳测试节点', 'pending')
	`, serverID)
	if err != nil {
		t.Fatalf("插入测试服务器：%v", err)
	}
	repository := server.NewPostgresRepository(db)
	receivedAt := time.Date(2026, 8, 28, 11, 0, 0, 0, time.UTC)

	accepted, err := repository.SaveHeartbeat(ctx, agentprotocol.Heartbeat{
		ServerID:        serverID,
		Sequence:        10,
		CPUTotalMilli:   8000,
		CPUUsedMilli:    3200,
		MemoryUsedBytes: 4 << 30,
		DiskFreeBytes:   20 << 30,
		RunningTasks:    1,
		Runtimes:        []string{"bash", "python3"},
		AgentVersion:    "0.1.0",
	}, receivedAt)
	if err != nil || !accepted {
		t.Fatalf("新心跳应被接受，accepted=%v err=%v", accepted, err)
	}
	accepted, err = repository.SaveHeartbeat(ctx, agentprotocol.Heartbeat{
		ServerID:     serverID,
		Sequence:     9,
		CPUUsedMilli: 900,
	}, receivedAt.Add(time.Second))
	if err != nil {
		t.Fatalf("保存旧心跳：%v", err)
	}
	if accepted {
		t.Fatal("旧序号心跳必须在数据库层被拒绝")
	}

	var sequence uint64
	var cpuTotalMilli int64
	var cpuUsedMilli int64
	var snapshotCount int
	if err := db.QueryRow(ctx, `SELECT last_heartbeat_sequence FROM servers WHERE id = $1`, serverID).Scan(&sequence); err != nil {
		t.Fatalf("读取服务器心跳序号：%v", err)
	}
	if err := db.QueryRow(ctx, `SELECT cpu_total_milli, cpu_used_milli FROM server_snapshots WHERE server_id = $1`, serverID).Scan(&cpuTotalMilli, &cpuUsedMilli); err != nil {
		t.Fatalf("读取服务器资源快照：%v", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM server_snapshots WHERE server_id = $1`, serverID).Scan(&snapshotCount); err != nil {
		t.Fatalf("统计服务器资源快照：%v", err)
	}
	if sequence != 10 || cpuTotalMilli != 8000 || cpuUsedMilli != 3200 || snapshotCount != 1 {
		t.Fatalf("旧心跳不得改变数据库，sequence=%d cpu_total=%d cpu_used=%d snapshots=%d", sequence, cpuTotalMilli, cpuUsedMilli, snapshotCount)
	}
}
