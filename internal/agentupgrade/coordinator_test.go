package agentupgrade

import (
	"context"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

func TestCoordinatorWaitsForTasksThenDispatchesCanary(t *testing.T) {
	store := coordinatorFixture()
	store.runtime["s1"] = ServerRuntime{Status: "online", Enabled: true, RunningTasks: 1}
	sender := &fakeUpgradeSender{}
	coordinator := NewCoordinator(store, sender, fixedCoordinatorNow)
	if err := coordinator.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.commands) != 0 || !store.runtime["s1"].Draining {
		t.Fatal("运行任务未结束时只能排空")
	}
	runtime := store.runtime["s1"]
	runtime.RunningTasks = 0
	store.runtime["s1"] = runtime
	if err := coordinator.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.commands) != 1 || sender.commands[0].TargetID != "target-canary" {
		t.Fatalf("未派发首批升级：%+v", sender.commands)
	}
}

func TestCoordinatorPausesAndRollsBackOnlyFailedBatch(t *testing.T) {
	store := coordinatorFixture()
	store.plan.CurrentBatch = 2
	store.plan.Targets = []Target{
		{ID: "canary", PlanID: "plan-1", ServerID: "s1", BatchNumber: 1, Status: TargetSucceeded},
		{ID: "batch-2-a", PlanID: "plan-1", ServerID: "s2", BatchNumber: 2, Status: TargetInstalling, CommandID: "cmd-a", SourceVersion: "0.1.0", TargetVersion: "0.2.0"},
		{ID: "batch-2-b", PlanID: "plan-1", ServerID: "s3", BatchNumber: 2, Status: TargetReconnecting, CommandID: "cmd-b", SourceVersion: "0.1.0", TargetVersion: "0.2.0"},
		{ID: "waiting", PlanID: "plan-1", ServerID: "s4", BatchNumber: 3, Status: TargetWaiting},
	}
	coordinator := NewCoordinator(store, &fakeUpgradeSender{}, fixedCoordinatorNow)
	err := coordinator.ApplyUpgradeEvent(context.Background(), "s2", agentprotocol.UpgradeEvent{TargetID: "batch-2-a", CommandID: "cmd-a", Stage: agentprotocol.StageFailed, ErrorCode: "stage_failed"})
	if err != nil {
		t.Fatal(err)
	}
	if store.plan.Status != PlanPaused {
		t.Fatalf("计划未暂停：%s", store.plan.Status)
	}
	if len(store.events) != 1 || store.events[0].Stage != string(agentprotocol.StageFailed) {
		t.Fatalf("代理事件未持久化：%+v", store.events)
	}
	assertCoordinatorStatus(t, store, "canary", TargetSucceeded)
	assertCoordinatorStatus(t, store, "batch-2-a", TargetRollingBack)
	assertCoordinatorStatus(t, store, "batch-2-b", TargetRollingBack)
	assertCoordinatorStatus(t, store, "waiting", TargetWaiting)
}

func TestCoordinatorMarksManualInterventionWhenRollbackFails(t *testing.T) {
	store := coordinatorFixture()
	coordinator := NewCoordinator(store, &fakeUpgradeSender{}, fixedCoordinatorNow)
	err := coordinator.ApplyUpgradeEvent(context.Background(), "s1", agentprotocol.UpgradeEvent{TargetID: "target-canary", CommandID: "command-1", Stage: agentprotocol.StageFailed, ErrorCode: "rollback_failed"})
	if err != nil {
		t.Fatal(err)
	}
	assertCoordinatorStatus(t, store, "target-canary", TargetManualIntervention)
	if !store.runtime["s1"].Draining {
		t.Fatal("回滚失败节点必须保持排空")
	}
}

func TestCoordinatorPausesOnDrainTimeoutAndWaitsForOfflineServer(t *testing.T) {
	now := fixedCoordinatorNow()
	store := coordinatorFixture()
	store.plan.Targets[0].Status = TargetDraining
	started := now.Add(-time.Hour - time.Second)
	store.plan.Targets[0].StartedAt = &started
	if err := NewCoordinator(store, &fakeUpgradeSender{}, func() time.Time { return now }).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.plan.Status != PlanPaused {
		t.Fatalf("排空超时必须暂停：%s", store.plan.Status)
	}

	store = coordinatorFixture()
	store.plan.Targets[0].Status = TargetDraining
	started = now.Add(-time.Minute)
	store.plan.Targets[0].StartedAt = &started
	runtime := store.runtime["s1"]
	runtime.Status = "offline"
	store.runtime["s1"] = runtime
	if err := NewCoordinator(store, &fakeUpgradeSender{}, func() time.Time { return now }).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.plan.Status != PlanRunning || store.plan.Targets[0].Status != TargetDraining {
		t.Fatalf("截止前离线节点应继续等待：%+v", store.plan)
	}
}

func TestCoordinatorReconcilesHeartbeatAndVerificationAfterRestart(t *testing.T) {
	now := fixedCoordinatorNow()
	store := coordinatorFixture()
	store.plan.Targets[0].Status = TargetReconnecting
	coordinator := NewCoordinator(store, &fakeUpgradeSender{}, func() time.Time { return now })
	if err := coordinator.ObserveHeartbeat(context.Background(), agentprotocol.Heartbeat{
		ServerID: "s1", AgentVersion: "0.2.0", Upgrade: &agentprotocol.UpgradeRuntimeState{CommandID: "command-1", TargetID: "target-canary"},
	}); err != nil {
		t.Fatal(err)
	}
	assertCoordinatorStatus(t, store, "target-canary", TargetHealthChecking)
	coordinator = NewCoordinator(store, &fakeUpgradeSender{}, func() time.Time { return now.Add(31 * time.Second) })
	if err := coordinator.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCoordinatorStatus(t, store, "target-canary", TargetSucceeded)
	if store.plan.Status != PlanSucceeded || store.runtime["s1"].Draining {
		t.Fatalf("健康窗口结束后应完成并恢复调度：%+v runtime=%+v", store.plan, store.runtime["s1"])
	}
}

func TestCoordinatorDoesNotSkipReconnectHealthWindowOnAgentSuccessEvent(t *testing.T) {
	store := coordinatorFixture()
	store.plan.Targets[0].Status = TargetInstalling
	coordinator := NewCoordinator(store, &fakeUpgradeSender{}, fixedCoordinatorNow)
	if err := coordinator.ApplyUpgradeEvent(context.Background(), "s1", agentprotocol.UpgradeEvent{
		TargetID: "target-canary", CommandID: "command-1", Stage: agentprotocol.StageSucceeded,
	}); err != nil {
		t.Fatal(err)
	}
	assertCoordinatorStatus(t, store, "target-canary", TargetReconnecting)
	if store.plan.Status != PlanRunning {
		t.Fatalf("代理成功事件不得绕过重连与健康窗口：%s", store.plan.Status)
	}
}

func TestCoordinatorCompletesRollbackWhenSourceVersionReconnects(t *testing.T) {
	store := coordinatorFixture()
	store.plan.Status = PlanPaused
	store.plan.Targets[0].Status = TargetRollingBack
	store.plan.Targets[0].CommandID = "rollback-command"
	sender := &fakeUpgradeSender{}
	coordinator := NewCoordinator(store, sender, fixedCoordinatorNow)
	if err := coordinator.ApplyUpgradeEvent(context.Background(), "s1", agentprotocol.UpgradeEvent{
		TargetID: "target-canary", CommandID: "rollback-command", Stage: agentprotocol.StageReconnecting,
	}); err != nil {
		t.Fatal(err)
	}
	assertCoordinatorStatus(t, store, "target-canary", TargetRollingBack)
	if err := coordinator.ObserveHeartbeat(context.Background(), agentprotocol.Heartbeat{
		ServerID: "s1", AgentVersion: "0.1.0", Upgrade: &agentprotocol.UpgradeRuntimeState{TargetID: "target-canary", CommandID: "rollback-command"},
	}); err != nil {
		t.Fatal(err)
	}
	assertCoordinatorStatus(t, store, "target-canary", TargetRolledBack)
	if len(sender.commands) != 0 {
		t.Fatalf("源版本重连后不得重复发送回滚命令：%+v", sender.commands)
	}
}

func TestCoordinatorFinalizesCancelledPlanAfterStartedTargetsFinish(t *testing.T) {
	store := coordinatorFixture()
	store.plan.Status = PlanPaused
	store.plan.CancelRequested = true
	store.plan.Targets = []Target{
		{ID: "finished", PlanID: "plan-1", ServerID: "s1", BatchNumber: 1, Status: TargetSucceeded},
		{ID: "cancelled", PlanID: "plan-1", ServerID: "s2", BatchNumber: 2, Status: TargetCancelled},
	}
	if err := NewCoordinator(store, &fakeUpgradeSender{}, fixedCoordinatorNow).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.plan.Status != PlanCancelled || store.plan.FinishedAt == nil {
		t.Fatalf("取消收尾结束后计划必须完成取消：%+v", store.plan)
	}
}

func TestCoordinatorFinishesDrainingTargetDuringCancelClosure(t *testing.T) {
	store := coordinatorFixture()
	store.plan.Status = PlanPaused
	store.plan.CancelRequested = true
	store.plan.Targets[0].Status = TargetDraining
	sender := &fakeUpgradeSender{}
	if err := NewCoordinator(store, sender, fixedCoordinatorNow).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sender.commands) != 1 || sender.commands[0].Action != agentprotocol.UpgradeInstall {
		t.Fatalf("取消收尾必须完成已经排空的目标：%+v", sender.commands)
	}
}

type coordinatorMemory struct {
	plan    Plan
	release ReleaseInfo
	runtime map[string]ServerRuntime
	events  []Event
}

func coordinatorFixture() *coordinatorMemory {
	now := fixedCoordinatorNow()
	return &coordinatorMemory{plan: Plan{ID: "plan-1", TargetReleaseID: "release-2", TargetVersion: "0.2.0", Status: PlanRunning, CurrentBatch: 1, DrainTimeoutSeconds: 3600, ReconnectTimeoutSeconds: 120, VerificationSeconds: 30, Targets: []Target{{ID: "target-canary", PlanID: "plan-1", ServerID: "s1", BatchNumber: 1, SourceVersion: "0.1.0", TargetVersion: "0.2.0", Status: TargetWaiting, CommandID: "command-1", UpdatedAt: now}}}, release: ReleaseInfo{ID: "release-2", Version: "0.2.0", Artifacts: []ArtifactInfo{{OS: "linux", Arch: "amd64", FileName: "agent.tar.gz", ByteSize: 10, SHA256: "digest", DownloadURL: "/agent.tar.gz"}}}, runtime: map[string]ServerRuntime{"s1": {Status: "online", Enabled: true, AgentOS: "linux", AgentArch: "amd64"}, "s2": {Status: "online", Enabled: true}, "s3": {Status: "online", Enabled: true}, "s4": {Status: "online", Enabled: true}}}
}
func (s *coordinatorMemory) ActivePlan(context.Context) (*Plan, error) {
	copy := s.plan
	return &copy, nil
}
func (s *coordinatorMemory) Plan(context.Context, string) (Plan, error) { return s.plan, nil }
func (s *coordinatorMemory) SavePlan(_ context.Context, p Plan) (Plan, error) {
	s.plan = p
	return p, nil
}
func (s *coordinatorMemory) Release(context.Context, string) (ReleaseInfo, error) {
	return s.release, nil
}
func (s *coordinatorMemory) ServerRuntime(_ context.Context, id string) (ServerRuntime, error) {
	return s.runtime[id], nil
}
func (s *coordinatorMemory) SetServerDraining(_ context.Context, id string, value bool) error {
	r := s.runtime[id]
	r.Draining = value
	if value && r.Status == "online" {
		r.Status = "draining"
	}
	s.runtime[id] = r
	return nil
}
func (s *coordinatorMemory) AppendEvent(_ context.Context, event Event) error {
	s.events = append(s.events, event)
	return nil
}

type sentUpgrade struct {
	serverID string
	command  agentprotocol.UpgradeCommand
}
type fakeUpgradeSender struct {
	commands []agentprotocol.UpgradeCommand
}

func (s *fakeUpgradeSender) SendUpgradeCommand(_ context.Context, _ string, c agentprotocol.UpgradeCommand) error {
	s.commands = append(s.commands, c)
	return nil
}
func fixedCoordinatorNow() time.Time { return time.Date(2026, 9, 7, 5, 0, 0, 0, time.UTC) }
func assertCoordinatorStatus(t *testing.T, s *coordinatorMemory, id string, want TargetStatus) {
	t.Helper()
	for _, target := range s.plan.Targets {
		if target.ID == id {
			if target.Status != want {
				t.Fatalf("目标 %s 状态=%s，期望=%s", id, target.Status, want)
			}
			return
		}
	}
	t.Fatalf("目标不存在：%s", id)
}
