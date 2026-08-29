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

func TestPostgresManagementUsesLatestSnapshotAndPersistsDrain(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000002_agent_enrollment.up.sql")
	testpostgres.ApplyMigration(t, db, "000003_server_management.up.sql")
	testpostgres.ApplyMigration(t, db, "000004_script_sync_states.up.sql")
	testpostgres.ApplyMigration(t, db, "000005_task_scheduling.up.sql")
	testpostgres.ApplyMigration(t, db, "000006_scheduler_resources.up.sql")
	testpostgres.ApplyMigration(t, db, "000008_security_audit_alerts.up.sql")
	ctx := context.Background()
	serverID := "123e4567-e89b-42d3-a456-426614174200"
	credentialHash := sha256.Sum256([]byte("agent-test-secret"))
	_, err := db.Exec(ctx, `
		INSERT INTO servers (id, name, cloud_provider, region, status, labels, runtimes, agent_version, last_seen_at)
		VALUES ($1, '京东云执行节点-1', '京东云', '华北', 'online', '{"用途":"批处理"}', '["bash","python3"]', '0.1.0', now())
	`, serverID)
	if err != nil {
		t.Fatalf("写入服务器测试数据：%v", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO agent_identities (id, server_id, credential_hash)
		VALUES ('123e4567-e89b-42d3-a456-426614174201', $1, $2)
	`, serverID, credentialHash[:])
	if err != nil {
		t.Fatalf("写入代理身份测试数据：%v", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO server_snapshots (
			server_id, cpu_usage_percent, memory_total_bytes, memory_available_bytes,
			disk_total_bytes, disk_available_bytes, running_tasks, collected_at
		) VALUES
			($1, 25, 17179869184, 12884901888, 107374182400, 85899345920, 0, '2026-08-28T03:00:00Z'),
			($1, 37.5, 17179869184, 10737418240, 107374182400, 75161927680, 1, '2026-08-28T04:00:00Z')
	`, serverID)
	if err != nil {
		t.Fatalf("写入服务器管理测试数据：%v", err)
	}
	repository := server.NewPostgresRepository(db)

	servers, err := repository.ListServers(ctx)
	if err != nil {
		t.Fatalf("读取服务器列表：%v", err)
	}
	if len(servers) != 1 || servers[0].CPUUsagePercent != 37.5 || servers[0].RunningTasks != 1 {
		t.Fatalf("列表必须使用最新资源快照：%+v", servers)
	}
	if !servers[0].Enabled || servers[0].SchedulingWeight != 100 {
		t.Fatalf("服务器默认应启用且调度权重为 100：%+v", servers[0])
	}

	draining := true
	updated, err := repository.UpdateServer(ctx, serverID, server.UpdateServerInput{Draining: &draining})
	if err != nil {
		t.Fatalf("请求排空服务器：%v", err)
	}
	if !updated.Draining || updated.Status != server.StatusDraining {
		t.Fatalf("排空状态未持久化：%+v", updated)
	}
	accepted, err := repository.SaveHeartbeat(ctx, agentprotocol.Heartbeat{
		ServerID:         serverID,
		Sequence:         1,
		CPUTotalMilli:    4000,
		CPUUsedMilli:     1000,
		MemoryTotalBytes: 16 << 30,
		MemoryUsedBytes:  4 << 30,
		DiskTotalBytes:   100 << 30,
		DiskFreeBytes:    70 << 30,
	}, time.Now())
	if err != nil || !accepted {
		t.Fatalf("排空后仍应接收心跳：accepted=%v err=%v", accepted, err)
	}
	if err := db.QueryRow(ctx, `SELECT status FROM servers WHERE id = $1`, serverID).Scan(&updated.Status); err != nil {
		t.Fatalf("读取心跳后状态：%v", err)
	}
	if updated.Status != server.StatusDraining {
		t.Fatalf("心跳不得解除排空，实际状态为 %s", updated.Status)
	}

	enabled := false
	if _, err := repository.UpdateServer(ctx, serverID, server.UpdateServerInput{Enabled: &enabled}); err != nil {
		t.Fatalf("停用服务器：%v", err)
	}
	if _, err := repository.FindServerByCredentialHash(ctx, credentialHash[:], time.Now()); !errors.Is(err, server.ErrAgentCredentialInvalid) {
		t.Fatalf("停用后必须拒绝代理重连，实际错误为 %v", err)
	}

	enabled = true
	if _, err := repository.UpdateServer(ctx, serverID, server.UpdateServerInput{Enabled: &enabled}); err != nil {
		t.Fatalf("恢复启用服务器：%v", err)
	}
	draining = true
	updated, err = repository.UpdateServer(ctx, serverID, server.UpdateServerInput{Draining: &draining})
	if err != nil {
		t.Fatalf("为离线服务器记录排空意图：%v", err)
	}
	if !updated.Draining || updated.Status != server.StatusOffline {
		t.Fatalf("离线服务器应保持离线并记录排空意图：%+v", updated)
	}
}
