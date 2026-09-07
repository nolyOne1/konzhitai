package agentupgrade

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCreatePlanMakesSingleNodeCanaryAndConfiguredBatches(t *testing.T) {
	repository := validMemoryRepository()
	service := NewService(repository)
	plan, err := service.CreatePlan(context.Background(), CreatePlanInput{
		TargetReleaseID: "release-2", ServerIDs: []string{"s1", "s2", "s3", "s4"}, BatchSize: 2, CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int, len(plan.Targets))
	for index := range plan.Targets {
		got[index] = plan.Targets[index].BatchNumber
	}
	if !reflect.DeepEqual(got, []int{1, 2, 2, 3}) {
		t.Fatalf("批次划分错误：%v", got)
	}
	if !plan.Targets[1].SourceDraining || plan.Targets[0].SourceDraining {
		t.Fatalf("未保留服务器原排空状态：%+v", plan.Targets)
	}
	if plan.DrainTimeoutSeconds != 3600 || plan.ReconnectTimeoutSeconds != 120 || plan.VerificationSeconds != 30 {
		t.Fatalf("默认超时不正确：%+v", plan)
	}
}

func TestCreatePlanValidation(t *testing.T) {
	for _, test := range []struct {
		name  string
		apply func(*memoryRepository, *CreatePlanInput)
		want  error
	}{
		{"已有活动计划", func(r *memoryRepository, _ *CreatePlanInput) { r.active = &Plan{ID: "active"} }, ErrActivePlanExists},
		{"重复服务器", func(_ *memoryRepository, in *CreatePlanInput) { in.ServerIDs = []string{"s1", "s1"} }, ErrInvalidPlan},
		{"超时无效", func(_ *memoryRepository, in *CreatePlanInput) { in.DrainTimeoutSeconds = 59 }, ErrInvalidPlan},
		{"服务器离线", func(r *memoryRepository, in *CreatePlanInput) { in.ServerIDs = []string{"offline"} }, ErrServerIneligible},
		{"服务器已停用", func(r *memoryRepository, in *CreatePlanInput) { in.ServerIDs = []string{"disabled"} }, ErrServerIneligible},
		{"缺少升级能力", func(r *memoryRepository, in *CreatePlanInput) { in.ServerIDs = []string{"legacy"} }, ErrUpgradeUnsupported},
		{"缺少匹配架构", func(r *memoryRepository, in *CreatePlanInput) { in.ServerIDs = []string{"unsupported-arch"} }, ErrArtifactUnavailable},
		{"已经是目标版本", func(r *memoryRepository, in *CreatePlanInput) { in.ServerIDs = []string{"already-current"} }, ErrNoUpgradeNeeded},
		{"目标版本没有升级器", func(r *memoryRepository, in *CreatePlanInput) { in.TargetReleaseID = "legacy-release" }, ErrUpgradeUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := validMemoryRepository()
			input := CreatePlanInput{TargetReleaseID: "release-2", ServerIDs: []string{"s1"}, BatchSize: 2, CreatedBy: "user-1"}
			test.apply(repository, &input)
			_, err := NewService(repository).CreatePlan(context.Background(), input)
			if !errors.Is(err, test.want) {
				t.Fatalf("错误=%v，期望=%v", err, test.want)
			}
		})
	}
}

func TestPauseResumeCancelRetryAndRollback(t *testing.T) {
	ctx := context.Background()
	repository := validMemoryRepository()
	service := NewService(repository)
	plan, err := service.CreatePlan(ctx, CreatePlanInput{TargetReleaseID: "release-2", ServerIDs: []string{"s1", "s2"}, CreatedBy: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if plan, err = service.Pause(ctx, plan.ID, "人工暂停"); err != nil || plan.Status != PlanPaused {
		t.Fatalf("暂停：%+v %v", plan, err)
	}
	if plan, err = service.Resume(ctx, plan.ID); err != nil || plan.Status != PlanRunning {
		t.Fatalf("继续：%+v %v", plan, err)
	}
	if plan, err = service.Cancel(ctx, plan.ID); err != nil || plan.Status != PlanCancelled || plan.Targets[0].Status != TargetCancelled {
		t.Fatalf("取消：%+v %v", plan, err)
	}

	stored := repository.plans[plan.ID]
	stored.Targets[0].Status = TargetRolledBack
	stored.Status = PlanPaused
	stored.CancelRequested = false
	repository.plans[plan.ID] = stored
	oldCommand := repository.plans[plan.ID].Targets[0].CommandID
	if plan, err = service.RetryTarget(ctx, plan.ID, plan.Targets[0].ID); err != nil || plan.Targets[0].Status != TargetDraining || plan.Targets[0].CommandID == oldCommand {
		t.Fatalf("重试：%+v %v", plan, err)
	}

	stored = repository.plans[plan.ID]
	stored.Targets[0].Status = TargetSucceeded
	stored.Status = PlanSucceeded
	repository.plans[plan.ID] = stored
	server := repository.servers["s1"]
	server.AgentVersion = "0.2.0"
	repository.servers["s1"] = server
	repository.active = nil
	rollback, err := service.CreateRollbackPlan(ctx, plan.ID, plan.Targets[0].ID, "user-1")
	if err != nil || rollback.TargetVersion != "0.1.0" || len(rollback.Targets) != 1 {
		t.Fatalf("创建回滚计划：%+v %v", rollback, err)
	}
}

func TestRollbackSucceededTargetInsideActivePlanStartsImmediately(t *testing.T) {
	ctx := context.Background()
	repository := validMemoryRepository()
	service := NewService(repository)
	plan, err := service.CreatePlan(ctx, CreatePlanInput{TargetReleaseID: "release-2", ServerIDs: []string{"s1", "s2"}, CreatedBy: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	stored := repository.plans[plan.ID]
	stored.Targets[0].Status = TargetSucceeded
	stored.Status = PlanPaused
	repository.plans[plan.ID] = stored
	repository.active = &stored
	rolled, err := service.CreateRollbackPlan(ctx, plan.ID, stored.Targets[0].ID, "user-1")
	if err != nil || rolled.Targets[0].Status != TargetRollingBack || rolled.Status != PlanPaused {
		t.Fatalf("活动计划内回滚失败：%+v %v", rolled, err)
	}
}

func TestPlanControlsAreIdempotent(t *testing.T) {
	ctx := context.Background()
	repository := validMemoryRepository()
	service := NewService(repository)
	plan, err := service.CreatePlan(ctx, CreatePlanInput{TargetReleaseID: "release-2", ServerIDs: []string{"s1"}, CreatedBy: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := service.Pause(ctx, plan.ID, "人工暂停")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := service.Pause(ctx, plan.ID, "重复请求"); err != nil || again.Status != paused.Status {
		t.Fatalf("重复暂停不幂等：%+v %v", again, err)
	}
	resumed, err := service.Resume(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := service.Resume(ctx, plan.ID); err != nil || again.Status != resumed.Status {
		t.Fatalf("重复继续不幂等：%+v %v", again, err)
	}
	cancelled, err := service.Cancel(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := service.Cancel(ctx, plan.ID); err != nil || again.Status != cancelled.Status {
		t.Fatalf("重复取消不幂等：%+v %v", again, err)
	}
}

func TestResumeRevalidatesWaitingServers(t *testing.T) {
	ctx := context.Background()
	repository := validMemoryRepository()
	service := NewService(repository)
	plan, err := service.CreatePlan(ctx, CreatePlanInput{TargetReleaseID: "release-2", ServerIDs: []string{"s1"}, CreatedBy: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Pause(ctx, plan.ID, "等待"); err != nil {
		t.Fatal(err)
	}
	server := repository.servers["s1"]
	server.Enabled = false
	repository.servers["s1"] = server
	if _, err := service.Resume(ctx, plan.ID); !errors.Is(err, ErrServerIneligible) {
		t.Fatalf("服务器条件变化后不得继续计划：%v", err)
	}
}

func TestCancelKeepsStartedTargetsManagedUntilTheyFinish(t *testing.T) {
	ctx := context.Background()
	repository := validMemoryRepository()
	service := NewService(repository)
	plan, err := service.CreatePlan(ctx, CreatePlanInput{TargetReleaseID: "release-2", ServerIDs: []string{"s1", "s2"}, CreatedBy: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	stored := repository.plans[plan.ID]
	stored.Targets[0].Status = TargetInstalling
	repository.plans[plan.ID] = stored
	cancelled, err := service.Cancel(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != PlanPaused || !cancelled.CancelRequested || cancelled.Targets[0].Status != TargetInstalling || cancelled.Targets[1].Status != TargetCancelled {
		t.Fatalf("取消后必须继续管理已开始目标：%+v", cancelled)
	}
	if _, err := service.Resume(ctx, plan.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("取消收尾中的计划不得继续：%v", err)
	}
}

func validMemoryRepository() *memoryRepository {
	servers := map[string]ServerInfo{}
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		servers[id] = ServerInfo{ID: id, Status: "online", Enabled: true, AgentVersion: "0.1.0", AgentOS: "linux", AgentArch: "amd64", Capabilities: []string{"self_upgrade_v1"}}
	}
	server := servers["s2"]
	server.Draining = true
	servers["s2"] = server
	servers["offline"] = ServerInfo{ID: "offline", Status: "offline", Enabled: true, AgentVersion: "0.1.0", AgentOS: "linux", AgentArch: "amd64", Capabilities: []string{"self_upgrade_v1"}}
	servers["disabled"] = ServerInfo{ID: "disabled", Status: "online", Enabled: false, AgentVersion: "0.1.0", AgentOS: "linux", AgentArch: "amd64", Capabilities: []string{"self_upgrade_v1"}}
	servers["legacy"] = ServerInfo{ID: "legacy", Status: "online", Enabled: true, AgentVersion: "0.1.0", AgentOS: "linux", AgentArch: "amd64"}
	servers["unsupported-arch"] = ServerInfo{ID: "unsupported-arch", Status: "online", Enabled: true, AgentVersion: "0.1.0", AgentOS: "linux", AgentArch: "s390x", Capabilities: []string{"self_upgrade_v1"}}
	servers["already-current"] = ServerInfo{ID: "already-current", Status: "online", Enabled: true, AgentVersion: "0.2.0", AgentOS: "linux", AgentArch: "amd64", Capabilities: []string{"self_upgrade_v1"}}
	return &memoryRepository{
		releases: map[string]ReleaseInfo{
			"release-1":      {ID: "release-1", Version: "0.1.0", Status: "available", Capabilities: []string{"self_upgrade_v1"}, Artifacts: []ArtifactInfo{{OS: "linux", Arch: "amd64"}}},
			"release-2":      {ID: "release-2", Version: "0.2.0", Status: "available", Capabilities: []string{"self_upgrade_v1"}, Artifacts: []ArtifactInfo{{OS: "linux", Arch: "amd64"}}},
			"legacy-release": {ID: "legacy-release", Version: "0.3.0", Status: "available", Artifacts: []ArtifactInfo{{OS: "linux", Arch: "amd64"}}},
		}, servers: servers, plans: map[string]Plan{},
	}
}

type memoryRepository struct {
	active   *Plan
	releases map[string]ReleaseInfo
	servers  map[string]ServerInfo
	plans    map[string]Plan
}

func (r *memoryRepository) ActivePlan(context.Context) (*Plan, error) { return r.active, nil }
func (r *memoryRepository) Release(_ context.Context, id string) (ReleaseInfo, error) {
	v, ok := r.releases[id]
	if !ok {
		return ReleaseInfo{}, ErrReleaseNotFound
	}
	return v, nil
}
func (r *memoryRepository) ReleaseByVersion(_ context.Context, version string) (ReleaseInfo, error) {
	for _, v := range r.releases {
		if v.Version == version {
			return v, nil
		}
	}
	return ReleaseInfo{}, ErrReleaseNotFound
}
func (r *memoryRepository) Servers(_ context.Context, ids []string) ([]ServerInfo, error) {
	out := make([]ServerInfo, 0, len(ids))
	for _, id := range ids {
		v, ok := r.servers[id]
		if !ok {
			return nil, ErrServerIneligible
		}
		out = append(out, v)
	}
	return out, nil
}
func (r *memoryRepository) CreatePlan(_ context.Context, plan Plan) (Plan, error) {
	r.plans[plan.ID] = plan
	if plan.Status == PlanRunning || plan.Status == PlanPending || plan.Status == PlanPaused {
		copy := plan
		r.active = &copy
	}
	return plan, nil
}
func (r *memoryRepository) ListPlans(context.Context) ([]Plan, error) {
	out := []Plan{}
	for _, p := range r.plans {
		out = append(out, p)
	}
	return out, nil
}
func (r *memoryRepository) Plan(_ context.Context, id string) (Plan, error) {
	p, ok := r.plans[id]
	if !ok {
		return Plan{}, ErrPlanNotFound
	}
	return p, nil
}
func (r *memoryRepository) SavePlan(_ context.Context, plan Plan) (Plan, error) {
	r.plans[plan.ID] = plan
	if plan.Status == PlanRunning || plan.Status == PlanPending || plan.Status == PlanPaused {
		copy := plan
		r.active = &copy
	} else {
		r.active = nil
	}
	return plan, nil
}
