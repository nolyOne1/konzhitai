package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/agentupgrade"
	"yunling.local/platform/internal/testpostgres"
)

func TestAgentUpgradeCanaryThenFailedBatchRollback(t *testing.T) {
	fixture := newUpgradeIntegrationFixture(t)
	plan := fixture.CreatePlan([]string{"server-1", "server-2", "server-3"}, 2)
	fixture.SetRunningTasks("server-1", 1)
	fixture.Scan()
	fixture.AssertNoCommand("server-1")
	fixture.SetRunningTasks("server-1", 0)
	fixture.Scan()
	fixture.AssertInstallCommand("server-1", plan.ID)
	fixture.SucceedAndVerify("server-1")
	fixture.Scan()
	fixture.AssertInstallCommand("server-2", plan.ID)
	fixture.AssertInstallCommand("server-3", plan.ID)
	fixture.Fail("server-2", "install_failed")
	fixture.AssertPlanStatus(plan.ID, "paused")
	fixture.AssertNoRollback("server-1")
	fixture.AssertRollback("server-2")
	fixture.AssertRollback("server-3")
}

type upgradeIntegrationFixture struct {
	t       *testing.T
	db      *pgxpool.Pool
	repo    *agentupgrade.PostgresRepository
	service *agentupgrade.Service
	worker  *agentupgrade.Coordinator
	sender  *integrationUpgradeSender
	now     time.Time
	tick    int
	servers map[string]string
}

func newUpgradeIntegrationFixture(t *testing.T) *upgradeIntegrationFixture {
	t.Helper()
	db := testpostgres.Start(t)
	for _, migration := range []string{
		"000001_initial.up.sql", "000002_agent_enrollment.up.sql", "000003_server_management.up.sql",
		"000004_script_sync_states.up.sql", "000005_task_scheduling.up.sql", "000006_scheduler_resources.up.sql",
		"000007_run_observability.up.sql", "000008_security_audit_alerts.up.sql", "000009_run_dispatch.up.sql",
		"000010_password_change_security.up.sql", "000011_notifications.up.sql", "000012_backup_recovery.up.sql",
		"000013_member_lifecycle.up.sql", "000014_agent_upgrade_management.up.sql",
	} { testpostgres.ApplyMigration(t, db, migration) }
	f := &upgradeIntegrationFixture{t: t, db: db, now: time.Date(2026, 9, 7, 6, 0, 0, 0, time.UTC), sender: &integrationUpgradeSender{}, servers: map[string]string{
		"server-1": "81000000-0000-4000-8000-000000000001", "server-2": "81000000-0000-4000-8000-000000000002", "server-3": "81000000-0000-4000-8000-000000000003",
	}}
	ctx := context.Background()
	const userID = "81000000-0000-4000-8000-000000000010"
	const releaseID = "81000000-0000-4000-8000-000000000020"
	if _, err := db.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash) VALUES($1,'upgrade@example.test','升级管理员','x')`, userID); err != nil { t.Fatal(err) }
	if _, err := db.Exec(ctx, `INSERT INTO agent_releases(id,version,status,recommended,manifest_sha256,capabilities) VALUES($1,'0.2.0','available',true,$2,'["self_upgrade_v1"]')`, releaseID, strings.Repeat("a", 64)); err != nil { t.Fatal(err) }
	if _, err := db.Exec(ctx, `INSERT INTO agent_release_artifacts(release_id,os,arch,file_name,byte_size,sha256,object_key) VALUES($1,'linux','amd64','agent.tar.gz',42,$2,'agents/0.2.0/agent.tar.gz')`, releaseID, strings.Repeat("b", 64)); err != nil { t.Fatal(err) }
	for alias, id := range f.servers {
		if _, err := db.Exec(ctx, `INSERT INTO servers(id,name,status,enabled,agent_version,agent_os,agent_arch,agent_capabilities) VALUES($1,$2,'online',true,'0.1.0','linux','amd64','["self_upgrade_v1"]')`, id, alias); err != nil { t.Fatal(err) }
		f.SetRunningTasks(alias, 0)
	}
	f.repo = agentupgrade.NewPostgresRepository(db)
	f.service = agentupgrade.NewService(f.repo)
	f.worker = agentupgrade.NewCoordinator(f.repo, f.sender, func() time.Time { return f.now })
	return f
}

func (f *upgradeIntegrationFixture) CreatePlan(aliases []string, batch int) agentupgrade.Plan {
	ids := make([]string, len(aliases)); for i, alias := range aliases { ids[i] = f.servers[alias] }
	plan, err := f.service.CreatePlan(context.Background(), agentupgrade.CreatePlanInput{TargetReleaseID: "81000000-0000-4000-8000-000000000020", ServerIDs: ids, BatchSize: batch, DrainTimeoutSeconds: 3600, ReconnectTimeoutSeconds: 120, VerificationSeconds: 30, CreatedBy: "81000000-0000-4000-8000-000000000010"})
	if err != nil { f.t.Fatal(err) }
	return plan
}

func (f *upgradeIntegrationFixture) SetRunningTasks(alias string, count int) {
	f.t.Helper(); f.tick++
	_, err := f.db.Exec(context.Background(), `INSERT INTO server_snapshots(server_id,cpu_usage_percent,memory_total_bytes,memory_available_bytes,disk_total_bytes,disk_available_bytes,running_tasks,collected_at) VALUES($1,10,8000,4000,10000,5000,$2,$3)`, f.servers[alias], count, f.now.Add(time.Duration(f.tick)*time.Millisecond))
	if err != nil { f.t.Fatal(err) }
}
func (f *upgradeIntegrationFixture) Scan() { f.t.Helper(); if err := f.worker.Scan(context.Background()); err != nil { f.t.Fatal(err) } }
func (f *upgradeIntegrationFixture) SucceedAndVerify(alias string) {
	f.t.Helper(); plan, err := f.repo.ActivePlan(context.Background()); if err != nil || plan == nil { f.t.Fatalf("读取活动计划：%v", err) }
	target := targetForServer(f.t, *plan, f.servers[alias])
	if err := f.worker.ApplyUpgradeEvent(context.Background(), target.ServerID, agentprotocol.UpgradeEvent{TargetID: target.ID, CommandID: target.CommandID, Stage: agentprotocol.StageReconnecting, OccurredAt: f.now}); err != nil { f.t.Fatal(err) }
	if err := f.worker.ObserveHeartbeat(context.Background(), agentprotocol.Heartbeat{ServerID: target.ServerID, AgentVersion: target.TargetVersion, Upgrade: &agentprotocol.UpgradeRuntimeState{TargetID: target.ID, CommandID: target.CommandID}}); err != nil { f.t.Fatal(err) }
	f.now = f.now.Add(31 * time.Second); f.Scan(); f.Scan()
}
func (f *upgradeIntegrationFixture) Fail(alias, code string) {
	f.t.Helper(); plan, err := f.repo.ActivePlan(context.Background()); if err != nil || plan == nil { f.t.Fatalf("读取活动计划：%v", err) }; target := targetForServer(f.t, *plan, f.servers[alias])
	if err := f.worker.ApplyUpgradeEvent(context.Background(), target.ServerID, agentprotocol.UpgradeEvent{TargetID: target.ID, CommandID: target.CommandID, Stage: agentprotocol.StageFailed, ErrorCode: code, Message: "安装失败", OccurredAt: f.now}); err != nil { f.t.Fatal(err) }
}
func (f *upgradeIntegrationFixture) AssertNoCommand(alias string) { f.t.Helper(); for _, item := range f.sender.commands { if item.serverID == f.servers[alias] { f.t.Fatalf("%s 不应收到命令：%+v", alias, item.command) } } }
func (f *upgradeIntegrationFixture) AssertInstallCommand(alias, planID string) { f.assertCommand(alias, planID, agentprotocol.UpgradeInstall, true) }
func (f *upgradeIntegrationFixture) AssertRollback(alias string) { f.assertCommand(alias, "", agentprotocol.UpgradeRollback, true) }
func (f *upgradeIntegrationFixture) AssertNoRollback(alias string) { f.assertCommand(alias, "", agentprotocol.UpgradeRollback, false) }
func (f *upgradeIntegrationFixture) assertCommand(alias, planID string, action agentprotocol.UpgradeAction, want bool) { f.t.Helper(); found := false; for _, item := range f.sender.commands { if item.serverID == f.servers[alias] && item.command.Action == action && (planID == "" || item.command.PlanID == planID) { found = true } }; if found != want { f.t.Fatalf("%s 命令 %s 存在=%v，期望=%v：%+v", alias, action, found, want, f.sender.commands) } }
func (f *upgradeIntegrationFixture) AssertPlanStatus(id, want string) { f.t.Helper(); plan, err := f.repo.Plan(context.Background(), id); if err != nil || string(plan.Status) != want { f.t.Fatalf("计划状态=%s，期望=%s err=%v", plan.Status, want, err) } }

type integrationUpgradeCommand struct { serverID string; command agentprotocol.UpgradeCommand }
type integrationUpgradeSender struct { commands []integrationUpgradeCommand }
func (s *integrationUpgradeSender) SendUpgradeCommand(_ context.Context, serverID string, command agentprotocol.UpgradeCommand) error { s.commands = append(s.commands, integrationUpgradeCommand{serverID: serverID, command: command}); return nil }
func targetForServer(t *testing.T, plan agentupgrade.Plan, serverID string) agentupgrade.Target { t.Helper(); for _, target := range plan.Targets { if target.ServerID == serverID { return target } }; t.Fatalf("计划中没有服务器 %s", serverID); return agentupgrade.Target{} }
