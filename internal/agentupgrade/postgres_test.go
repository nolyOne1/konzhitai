package agentupgrade

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresRepositoryPersistsPlanAndTargetsAtomically(t *testing.T) {
	db := testpostgres.Start(t)
	for _, migration := range []string{
		"000001_initial.up.sql", "000002_agent_enrollment.up.sql", "000003_server_management.up.sql",
		"000004_script_sync_states.up.sql", "000005_task_scheduling.up.sql", "000006_scheduler_resources.up.sql",
		"000007_run_observability.up.sql", "000008_security_audit_alerts.up.sql", "000009_run_dispatch.up.sql",
		"000010_password_change_security.up.sql", "000011_notifications.up.sql", "000012_backup_recovery.up.sql",
		"000013_member_lifecycle.up.sql", "000014_agent_upgrade_management.up.sql",
	} {
		testpostgres.ApplyMigration(t, db, migration)
	}
	ctx := context.Background()
	userID, serverID, releaseID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash) VALUES($1,$2,'测试管理员','x')`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO servers(id,name,status,enabled,agent_version,agent_os,agent_arch,agent_capabilities) VALUES($1,$2,'online',true,'0.1.0','linux','amd64','["self_upgrade_v1"]')`, serverID, "server-"+serverID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_releases(id,version,status,manifest_sha256,capabilities) VALUES($1,'0.2.0','available',$2,'["self_upgrade_v1"]')`, releaseID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_release_artifacts(release_id,os,arch,file_name,byte_size,sha256,object_key) VALUES($1,'linux','amd64','agent.tar.gz',1,$2,$3)`, releaseID, strings.Repeat("b", 64), "agent/"+releaseID); err != nil {
		t.Fatal(err)
	}

	repository := NewPostgresRepository(db, "https://control.example/")
	service := NewService(repository)
	release, err := repository.Release(ctx, releaseID)
	if err != nil || len(release.Artifacts) != 1 || !strings.HasPrefix(release.Artifacts[0].DownloadURL, "https://control.example/api/releases/agent/") {
		t.Fatalf("代理安装包必须使用绝对同源地址：%+v err=%v", release.Artifacts, err)
	}
	plan, err := service.CreatePlan(ctx, CreatePlanInput{TargetReleaseID: releaseID, ServerIDs: []string{serverID}, CreatedBy: userID})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := service.Plan(ctx, plan.ID)
	if err != nil || len(loaded.Targets) != 1 || loaded.Targets[0].ServerID != serverID {
		t.Fatalf("读取计划失败：%+v err=%v", loaded, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO agent_upgrade_events(id,plan_id,target_id,server_id,command_id,stage,message,occurred_at) VALUES($1,$2,$3,$4,$5,'downloading','正在下载安装包',$6)`, uuid.NewString(), plan.ID, plan.Targets[0].ID, serverID, plan.Targets[0].CommandID, time.Date(2026, 9, 7, 6, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	loaded, err = service.Plan(ctx, plan.ID)
	if err != nil || loaded.Targets[0].ServerName != "server-"+serverID || len(loaded.Events) != 1 || loaded.Events[0].Message != "正在下载安装包" {
		t.Fatalf("计划详情应包含服务器名称和事件：%+v err=%v", loaded, err)
	}
	stale := loaded
	loaded.Targets[0].Status = TargetDraining
	loaded.Targets[0].UpdatedAt = time.Date(2026, 9, 7, 6, 1, 0, 0, time.UTC)
	transitioned, err := NewPostgresRepository(db).SavePlan(ctx, loaded)
	if err != nil || len(transitioned.Events) != 2 || transitioned.Events[1].Stage != string(TargetDraining) {
		t.Fatalf("目标状态变化必须追加事件：plan=%+v err=%v", transitioned, err)
	}
	if _, err := service.CreatePlan(ctx, CreatePlanInput{TargetReleaseID: releaseID, ServerIDs: []string{serverID}, CreatedBy: userID}); !errors.Is(err, ErrActivePlanExists) {
		t.Fatalf("活动计划唯一约束未映射：%v", err)
	}
	paused, err := service.Pause(ctx, plan.ID, "检查")
	if err != nil || paused.Status != PlanPaused {
		t.Fatalf("持久化暂停失败：%+v err=%v", paused, err)
	}
	if paused.Revision != transitioned.Revision+1 {
		t.Fatalf("保存计划必须推进并发版本：before=%d after=%d", transitioned.Revision, paused.Revision)
	}
	stale.Status = PlanCancelled
	if _, err := NewPostgresRepository(db).SavePlan(ctx, stale); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("旧计划副本不得覆盖较新状态：%v", err)
	}
}
