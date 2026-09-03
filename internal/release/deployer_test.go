package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeLocker struct {
	err      error
	calls    []string
	released int
}

func (locker *fakeLocker) TryLock(path string) (func() error, error) {
	locker.calls = append(locker.calls, path)
	if locker.err != nil {
		return nil, locker.err
	}
	return func() error {
		locker.released++
		return nil
	}, nil
}

type fakeHealthChecker struct {
	waits  []error
	calls  int
	times  []time.Duration
	checks int
}

func (checker *fakeHealthChecker) CheckOnce(context.Context) error {
	checker.checks++
	return nil
}

func (checker *fakeHealthChecker) Wait(_ context.Context, timeout, interval time.Duration) error {
	checker.times = append(checker.times, timeout, interval)
	index := checker.calls
	checker.calls++
	if index < len(checker.waits) {
		return checker.waits[index]
	}
	return nil
}

type deployRunner struct {
	calls          []commandCall
	images         map[string]string
	pullFailure    string
	digestMismatch string
	infrastructure []byte
	composeUpError error
	composeUpCalls int
	diagnosticText string
}

func (runner *deployRunner) Run(_ context.Context, name string, args []string, stdin []byte) (CommandResult, error) {
	runner.calls = append(runner.calls, commandCall{
		name: name, args: append([]string(nil), args...), stdin: append([]byte(nil), stdin...),
	})
	joined := strings.Join(args, " ")
	if reflect.DeepEqual(args, []string{"version"}) || reflect.DeepEqual(args, []string{"compose", "version"}) {
		return CommandResult{}, nil
	}
	if strings.Contains(joined, " ps --format json postgres redis minio caddy") {
		if runner.infrastructure != nil {
			return CommandResult{Stdout: runner.infrastructure}, nil
		}
		return CommandResult{Stdout: healthyInfrastructureJSON()}, nil
	}
	if len(args) == 2 && args[0] == "pull" {
		if args[1] == runner.pullFailure {
			return CommandResult{ExitCode: 1, Stderr: []byte("pull failed")}, errors.New("pull failed")
		}
		return CommandResult{}, nil
	}
	if len(args) == 5 && reflect.DeepEqual(args[:4], []string{"image", "inspect", "--format", "{{json .RepoDigests}}"}) {
		image := args[4]
		if image == runner.digestMismatch {
			return CommandResult{Stdout: []byte(`["ghcr.io/nolyone1/yunling-web@sha256:` + strings.Repeat("f", 64) + `"]`)}, nil
		}
		return CommandResult{Stdout: []byte(`["` + image + `"]`)}, nil
	}
	if strings.Contains(joined, " up -d --no-deps --no-build api scheduler web ops") {
		runner.composeUpCalls++
		if runner.composeUpCalls == 1 && runner.composeUpError != nil {
			return CommandResult{ExitCode: 1}, runner.composeUpError
		}
		return CommandResult{}, nil
	}
	if len(args) == 4 && args[0] == "logs" && args[1] == "--tail" && args[2] == "200" {
		return CommandResult{Stdout: []byte(runner.diagnosticText)}, nil
	}
	if strings.Contains(joined, " ps --format json api scheduler web ops") {
		return CommandResult{Stdout: []byte(`[]`)}, nil
	}
	return CommandResult{}, errors.New("未预期命令：docker " + joined)
}

func TestDeployExecutesPinnedImagesThenCommitsState(t *testing.T) {
	fixture := newDeploymentFixture(t)
	result, err := fixture.deployer.Execute(context.Background(), validDeployRequest(fixture.manifest))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.RollbackStatus != "not-required" || result.TargetID != "101" {
		t.Fatalf("部署结果错误：%+v", result)
	}
	if fixture.locker.released != 1 || len(fixture.locker.calls) != 1 {
		t.Fatalf("发布锁未正确释放：%+v", fixture.locker)
	}
	if fixture.health.calls != 1 || !reflect.DeepEqual(fixture.health.times, []time.Duration{2 * time.Minute, 5 * time.Second}) {
		t.Fatalf("健康检查窗口错误：calls=%d times=%v", fixture.health.calls, fixture.health.times)
	}
	assertDeployCommandOrder(t, fixture.runner.calls, fixture.manifest)
	current, err := fixture.store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	previous, err := fixture.store.LoadPrevious()
	if err != nil {
		t.Fatal(err)
	}
	if current.TargetID != "101" || previous.TargetID != "bootstrap" {
		t.Fatalf("成功状态错误：current=%s previous=%s", current.TargetID, previous.TargetID)
	}
	override, err := os.ReadFile(fixture.config.OverrideFile)
	if err != nil {
		t.Fatal(err)
	}
	wantOverride, err := RenderComposeOverride(current)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(override, wantOverride) {
		t.Fatalf("发布覆盖文件错误：\n%s", override)
	}
}

func TestDeployHealthFailureRestoresPreviousRelease(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.health.waits = []error{errors.New("web unhealthy"), nil}
	fixture.runner.diagnosticText = "DATABASE_URL=postgres://admin:secret@postgres/yunling\nTOKEN=very-secret\n普通诊断\n"
	result, err := fixture.deployer.Execute(context.Background(), validDeployRequest(fixture.manifest))
	if err == nil {
		t.Fatal("健康失败的部署必须返回错误")
	}
	if result.Status != "failed" || result.RollbackStatus != "succeeded" || result.DiagnosticID == "" {
		t.Fatalf("自动回滚结果错误：%+v", result)
	}
	if fixture.runner.composeUpCalls != 2 || fixture.health.calls != 2 {
		t.Fatalf("必须部署一次并恢复一次：up=%d health=%d", fixture.runner.composeUpCalls, fixture.health.calls)
	}
	current, err := fixture.store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if current.TargetID != "bootstrap" {
		t.Fatalf("失败候选不得成为当前版本：%s", current.TargetID)
	}
	override, err := os.ReadFile(fixture.config.OverrideFile)
	if err != nil {
		t.Fatal(err)
	}
	want, err := RenderComposeOverride(current)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(override, want) {
		t.Fatal("自动回滚后覆盖文件没有恢复当前成功版本")
	}
	diagnostics, err := readDiagnosticTree(filepath.Join(fixture.store.root, "diagnostics", result.DiagnosticID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diagnostics, "very-secret") || strings.Contains(diagnostics, "admin:secret") {
		t.Fatalf("诊断信息泄露秘密：%s", diagnostics)
	}
	if !strings.Contains(diagnostics, "普通诊断") {
		t.Fatal("诊断信息不应删除普通日志")
	}
}

func TestManualRollbackLoadsOnlySuccessfulHistoryAndRotatesState(t *testing.T) {
	fixture := newDeploymentFixture(t)
	first, err := fixture.deployer.Execute(context.Background(), validDeployRequest(fixture.manifest))
	if err != nil || first.Status != "succeeded" {
		t.Fatalf("准备首个版本失败：result=%+v err=%v", first, err)
	}

	fixture.runner.calls = nil
	fixture.runner.composeUpCalls = 0
	fixture.health.calls = 0
	request := validRollbackRequest("bootstrap")
	result, err := fixture.deployer.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.RollbackStatus != "not-required" {
		t.Fatalf("人工回滚结果错误：%+v", result)
	}
	if fixture.runner.composeUpCalls != 1 || fixture.health.calls != 1 {
		t.Fatalf("人工回滚只能切换并检查一次：up=%d health=%d", fixture.runner.composeUpCalls, fixture.health.calls)
	}
	for _, call := range fixture.runner.calls {
		if len(call.args) > 0 && (call.args[0] == "pull" || call.args[0] == "image") {
			t.Fatalf("本地成功历史回滚不得访问镜像仓库：%v", call.args)
		}
	}
	current, err := fixture.store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	previous, err := fixture.store.LoadPrevious()
	if err != nil {
		t.Fatal(err)
	}
	if current.TargetID != "bootstrap" || previous.TargetID != "101" {
		t.Fatalf("人工回滚轮换错误：current=%s previous=%s", current.TargetID, previous.TargetID)
	}
}

func TestDeployRejectsInvalidRequestBeforeLockOrDocker(t *testing.T) {
	fixture := newDeploymentFixture(t)
	request := validDeployRequest(fixture.manifest)
	request.WorkflowURL = "https://evil.example/actions/runs/9001"
	result, err := fixture.deployer.Execute(context.Background(), request)
	if err == nil || result.Status != "failed" || result.DiagnosticID == "" {
		t.Fatalf("非法请求结果错误：result=%+v err=%v", result, err)
	}
	if len(fixture.locker.calls) != 0 || len(fixture.runner.calls) != 0 {
		t.Fatal("非法请求不得获取发布锁或调用 Docker")
	}
}

func TestDeployRejectsCompatibilityAndDigestMismatchBeforeCompose(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*deploymentFixture)
	}{
		{name: "兼容性不一致", mutate: func(fixture *deploymentFixture) {
			fixture.manifest.Compatibility.AgentManifestSHA256 = strings.Repeat("9", 64)
		}},
		{name: "本地摘要不一致", mutate: func(fixture *deploymentFixture) {
			fixture.runner.digestMismatch = fixture.manifest.Images.Web
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			test.mutate(fixture)
			result, err := fixture.deployer.Execute(context.Background(), validDeployRequest(fixture.manifest))
			if err == nil || result.Status != "failed" || result.RollbackStatus != "not-required" {
				t.Fatalf("边界失败结果错误：result=%+v err=%v", result, err)
			}
			if fixture.runner.composeUpCalls != 0 || fixture.health.calls != 0 {
				t.Fatal("预更新校验失败不得更新或健康检查")
			}
		})
	}
}

func TestDeployPreUpdateFailuresNeverAttemptRollback(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*deploymentFixture)
	}{
		{name: "锁冲突", mutate: func(fixture *deploymentFixture) {
			fixture.locker.err = ErrLockHeld
		}},
		{name: "磁盘不足", mutate: func(fixture *deploymentFixture) {
			fixture.deployer.Resources = fakeResources{free: 3<<30 - 1, memory: 1 << 30}
		}},
		{name: "基础设施异常", mutate: func(fixture *deploymentFixture) {
			fixture.runner.infrastructure = []byte(`[
				{"Service":"postgres","State":"running","Health":"healthy"},
				{"Service":"redis","State":"running","Health":"unhealthy"},
				{"Service":"minio","State":"running","Health":"healthy"},
				{"Service":"caddy","State":"running","Health":"healthy"}
			]`)
		}},
		{name: "镜像拉取失败", mutate: func(fixture *deploymentFixture) {
			fixture.runner.pullFailure = fixture.manifest.Images.Web
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			test.mutate(fixture)
			result, err := fixture.deployer.Execute(context.Background(), validDeployRequest(fixture.manifest))
			if err == nil || result.Status != "failed" || result.RollbackStatus != "not-required" || result.DiagnosticID == "" {
				t.Fatalf("预更新失败结果错误：result=%+v err=%v", result, err)
			}
			if fixture.runner.composeUpCalls != 0 || fixture.health.calls != 0 {
				t.Fatalf("预更新失败不得更新或回滚：up=%d health=%d", fixture.runner.composeUpCalls, fixture.health.calls)
			}
		})
	}
}

func TestEveryPostUpdateHealthFailureTriggersAutomaticRollback(t *testing.T) {
	for _, layer := range []string{"api", "scheduler", "web", "ops", "public-healthz", "public-api-health"} {
		t.Run(layer, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			fixture.health.waits = []error{errors.New(layer + " unhealthy"), nil}
			result, err := fixture.deployer.Execute(context.Background(), validDeployRequest(fixture.manifest))
			if err == nil || result.RollbackStatus != "succeeded" {
				t.Fatalf("健康层 %s 未触发成功恢复：result=%+v err=%v", layer, result, err)
			}
			if fixture.runner.composeUpCalls != 2 || fixture.health.calls != 2 {
				t.Fatalf("健康层 %s 的更新/恢复次数错误：up=%d health=%d", layer, fixture.runner.composeUpCalls, fixture.health.calls)
			}
		})
	}
}

func TestDeployReportsCriticalFailureWhenAutomaticRollbackIsUnhealthy(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.health.waits = []error{errors.New("candidate unhealthy"), errors.New("rollback unhealthy")}
	result, err := fixture.deployer.Execute(context.Background(), validDeployRequest(fixture.manifest))
	if err == nil {
		t.Fatal("自动回滚失败必须返回错误")
	}
	if result.Status != "failed" || result.RollbackStatus != "failed" || result.DiagnosticID == "" {
		t.Fatalf("严重故障结果错误：%+v", result)
	}
	if fixture.runner.composeUpCalls != 2 || fixture.health.calls != 2 {
		t.Fatalf("严重故障仍必须尝试一次恢复：up=%d health=%d", fixture.runner.composeUpCalls, fixture.health.calls)
	}
}

func TestDeploymentDiagnosticsEnforceLineAndAggregateLimits(t *testing.T) {
	fixture := newDeploymentFixture(t)
	fixture.health.waits = []error{errors.New("candidate unhealthy"), nil}
	fixture.runner.diagnosticText = strings.Repeat("普通日志行\n", 100000)
	result, err := fixture.deployer.Execute(context.Background(), validDeployRequest(fixture.manifest))
	if err == nil || result.DiagnosticID == "" {
		t.Fatalf("应生成失败诊断：result=%+v err=%v", result, err)
	}
	root := filepath.Join(fixture.store.root, "diagnostics", result.DiagnosticID)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		total += len(content)
		if entry.Name() != "compose-ps.log" && bytesCountLines(content) > 200 {
			t.Fatalf("%s 保存了 %d 行，超过 200 行", entry.Name(), bytesCountLines(content))
		}
	}
	if total > maxDiagnosticBytes {
		t.Fatalf("诊断合计=%d，超过 %d", total, maxDiagnosticBytes)
	}
}

func TestRollbackRejectsUnknownOrUnsuccessfulHistory(t *testing.T) {
	for _, target := range []string{"999", "102"} {
		t.Run(target, func(t *testing.T) {
			fixture := newDeploymentFixture(t)
			if target == "102" {
				candidate := testGHCRRelease(t, 102)
				if err := fixture.store.SaveValidated(candidate); err != nil {
					t.Fatal(err)
				}
			}
			result, err := fixture.deployer.Execute(context.Background(), validRollbackRequest(target))
			if err == nil || result.Status != "failed" {
				t.Fatalf("未知或失败历史必须被拒绝：result=%+v err=%v", result, err)
			}
			if len(fixture.runner.calls) != 0 {
				t.Fatal("历史校验失败不得调用 Docker")
			}
		})
	}
}

func TestDeployerNeverRunsDestructiveOrOutOfScopeCommands(t *testing.T) {
	fixture := newDeploymentFixture(t)
	_, _ = fixture.deployer.Execute(context.Background(), validDeployRequest(fixture.manifest))
	for _, call := range fixture.runner.calls {
		command := " " + strings.Join(call.args, " ") + " "
		for _, forbidden := range []string{" down ", " restart ", " volume ", " bootstrap ", " migrate ", " migration ", " build "} {
			if strings.Contains(command, forbidden) {
				t.Fatalf("出现禁止命令 %q：docker%s", forbidden, command)
			}
		}
	}
}

type deploymentFixture struct {
	deployer Deployer
	store    *StateStore
	runner   *deployRunner
	locker   *fakeLocker
	health   *fakeHealthChecker
	config   HostConfig
	manifest Manifest
}

func newDeploymentFixture(t *testing.T) *deploymentFixture {
	t.Helper()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "releases")
	store := NewStateStore(stateRoot)
	manifest := validManifest()
	manifest.CandidateRunID = 101
	bootstrapImages := ServiceImages{
		API:       "yunling-local-bootstrap/api:111111111111",
		Scheduler: "yunling-local-bootstrap/scheduler:222222222222",
		Web:       "yunling-local-bootstrap/web:333333333333",
		Ops:       "yunling-local-bootstrap/ops:444444444444",
	}
	if _, err := store.CreateBootstrap(bootstrapImages, manifest.Compatibility, time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	config := HostConfig{
		RootDir: root, ComposeFile: filepath.Join(root, "docker-compose.yml"),
		OverrideFile: filepath.Join(root, "docker-compose.release.yml"),
		EnvFile:      filepath.Join(root, ".env"), ProjectName: "yunling",
		PublicBaseURL: "https://aiwise.top",
	}
	runner := &deployRunner{}
	locker := &fakeLocker{}
	health := &fakeHealthChecker{}
	deployer := Deployer{
		Config: config, Policy: ManifestPolicy{RepositoryID: 42, Owner: "nolyone1"},
		Store: store, Runner: runner, Resources: fakeResources{free: 4 << 30, memory: 1 << 30},
		Locker: locker, Health: health,
		Now: func() time.Time { return time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC) },
	}
	return &deploymentFixture{
		deployer: deployer, store: store, runner: runner, locker: locker,
		health: health, config: config, manifest: manifest,
	}
}

func validDeployRequest(manifest Manifest) Request {
	return Request{
		Operation: OperationDeploy, TargetID: strconv.FormatInt(manifest.CandidateRunID, 10),
		Actor: "nolyOne1", WorkflowRunID: 9001,
		WorkflowURL: "https://github.com/nolyOne1/konzhitai/actions/runs/9001",
		Manifest:    &manifest,
	}
}

func validRollbackRequest(target string) Request {
	return Request{
		Operation: OperationRollback, TargetID: target, Actor: "nolyOne1",
		WorkflowRunID: 9002, WorkflowURL: "https://github.com/nolyOne1/konzhitai/actions/runs/9002",
	}
}

func healthyInfrastructureJSON() []byte {
	return []byte(`[
		{"Service":"postgres","State":"running","Health":"healthy"},
		{"Service":"redis","State":"running","Health":"healthy"},
		{"Service":"minio","State":"running","Health":"healthy"},
		{"Service":"caddy","State":"running","Health":"healthy"}
	]`)
}

func assertDeployCommandOrder(t *testing.T, calls []commandCall, manifest Manifest) {
	t.Helper()
	configPrefixLength := 9
	if len(calls) != 10 {
		t.Fatalf("部署命令数=%d，期望=10：%#v", len(calls), calls)
	}
	if !reflect.DeepEqual(calls[0].args, []string{"version"}) || !reflect.DeepEqual(calls[1].args, []string{"compose", "version"}) {
		t.Fatalf("Docker 能力检查顺序错误：%#v", calls[:2])
	}
	if len(calls[2].args) < configPrefixLength || !reflect.DeepEqual(calls[2].args[len(calls[2].args)-7:], []string{"ps", "--format", "json", "postgres", "redis", "minio", "caddy"}) {
		t.Fatalf("基础设施检查错误：%v", calls[2].args)
	}
	images := []string{manifest.Images.Services, manifest.Images.Web, manifest.Images.Ops}
	for index, image := range images {
		if !reflect.DeepEqual(calls[3+index].args, []string{"pull", image}) {
			t.Fatalf("镜像拉取 %d 错误：%v", index, calls[3+index].args)
		}
		if !reflect.DeepEqual(calls[6+index].args, []string{"image", "inspect", "--format", "{{json .RepoDigests}}", image}) {
			t.Fatalf("镜像摘要检查 %d 错误：%v", index, calls[6+index].args)
		}
	}
	if !reflect.DeepEqual(calls[9].args[len(calls[9].args)-8:], []string{"up", "-d", "--no-deps", "--no-build", "api", "scheduler", "web", "ops"}) {
		t.Fatalf("应用更新命令错误：%v", calls[9].args)
	}
}

func readDiagnosticTree(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return "", err
		}
		output.Write(content)
	}
	return output.String(), nil
}

func bytesCountLines(content []byte) int {
	return strings.Count(string(content), "\n")
}
