package postgres_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"yunling.local/platform/internal/testpostgres"
)

func TestAgentUpgradeRecoveryMigrationBackfillsExistingTargets(t *testing.T) {
	db := testpostgres.Start(t)
	root := testpostgres.RepositoryRoot(t)
	migrations, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		name := filepath.Base(migration)
		if name >= "000015_" {
			continue
		}
		testpostgres.ApplyMigration(t, db, name)
	}
	ctx := context.Background()
	userID, serverID, releaseID, planID, targetID, commandID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash) VALUES($1,$2,'升级管理员','x')`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO servers(id,name,status) VALUES($1,$2,'online')`, serverID, "server-"+serverID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_releases(id,version,manifest_sha256) VALUES($1,'0.2.0',$2)`, releaseID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_upgrade_plans(id,target_release_id,status,batch_size,drain_timeout_seconds,reconnect_timeout_seconds,verification_seconds,created_by) VALUES($1,$2,'running',1,3600,120,30,$3)`, planID, releaseID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_upgrade_targets(id,plan_id,server_id,batch_number,source_version,target_version,source_draining,status,command_id) VALUES($1,$2,$3,1,'0.1.0','0.2.0',false,'installing',$4)`, targetID, planID, serverID, commandID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_upgrade_events(plan_id,target_id,server_id,command_id,stage,occurred_at) VALUES($1,$2,$3,$4,'installing',now()),($1,$2,$3,$4,'installing',now())`, planID, targetID, serverID, commandID); err != nil {
		t.Fatal(err)
	}

	testpostgres.ApplyMigration(t, db, "000015_agent_upgrade_recovery.up.sql")
	var installCommandID string
	if err := db.QueryRow(ctx, `SELECT install_command_id::text FROM agent_upgrade_targets WHERE id=$1`, targetID).Scan(&installCommandID); err != nil || installCommandID != commandID {
		t.Fatalf("现有升级目标未绑定原安装命令：got=%s want=%s err=%v", installCommandID, commandID, err)
	}
	var eventCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM agent_upgrade_events WHERE target_id=$1 AND command_id=$2 AND stage='installing'`, targetID, commandID).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("升级迁移未清理历史重复事件：count=%d err=%v", eventCount, err)
	}
	var version int
	if err := db.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 15 {
		t.Fatalf("升级后 schema 版本错误：version=%d err=%v", version, err)
	}
}
