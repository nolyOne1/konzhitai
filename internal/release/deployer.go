package release

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	releaseHealthTimeout  = 2 * time.Minute
	releaseHealthInterval = 5 * time.Second
	maxDiagnosticBytes    = 1 << 20
)

var (
	ErrInvalidRequest      = errors.New("生产发布请求无效")
	ErrIncompatibleRelease = errors.New("候选版本与生产基线不兼容")
	ErrImageDigestMismatch = errors.New("本地镜像摘要与候选清单不一致")
	actorPattern           = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
)

type Operation string

const (
	OperationDeploy   Operation = "deploy"
	OperationRollback Operation = "rollback"
)

type Request struct {
	Operation     Operation `json:"operation"`
	TargetID      string    `json:"target_id"`
	Actor         string    `json:"actor"`
	WorkflowRunID int64     `json:"workflow_run_id"`
	WorkflowURL   string    `json:"workflow_url"`
	Manifest      *Manifest `json:"manifest,omitempty"`
}

type Result struct {
	Operation      Operation `json:"operation"`
	TargetID       string    `json:"target_id"`
	SourceSHA      string    `json:"source_sha,omitempty"`
	Status         string    `json:"status"`
	RollbackStatus string    `json:"rollback_status,omitempty"`
	DiagnosticID   string    `json:"diagnostic_id,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
}

type Deployer struct {
	Config    HostConfig
	Policy    ManifestPolicy
	Store     *StateStore
	Runner    CommandRunner
	Resources ResourceReader
	Locker    Locker
	Health    HealthChecker
	Now       func() time.Time
}

func (deployer *Deployer) Execute(ctx context.Context, request Request) (Result, error) {
	startedAt := deployer.now()
	result := Result{
		Operation: request.Operation, TargetID: request.TargetID,
		Status: "failed", RollbackStatus: "not-required", StartedAt: startedAt,
	}
	fail := func(cause error) (Result, error) {
		result.FinishedAt = deployer.now()
		if result.DiagnosticID == "" {
			result.DiagnosticID = makeDiagnosticID(startedAt, request.WorkflowRunID, request.TargetID)
		}
		return result, cause
	}

	if ctx == nil {
		return fail(fmt.Errorf("%w：上下文为空", ErrInvalidRequest))
	}
	if deployer == nil {
		return fail(fmt.Errorf("%w：发布器为空", ErrInvalidRequest))
	}
	if err := validateDeploymentRequest(request, deployer.Policy); err != nil {
		return fail(err)
	}
	if deployer.Store == nil || deployer.Store.root == "" || deployer.Runner == nil || deployer.Resources == nil || deployer.Locker == nil || deployer.Health == nil {
		return fail(fmt.Errorf("%w：发布依赖不完整", ErrInvalidRequest))
	}

	releaseLock, err := deployer.Locker.TryLock(filepath.Join(deployer.Store.root, "release.lock"))
	if err != nil {
		return fail(fmt.Errorf("获取生产发布锁：%w", err))
	}
	if releaseLock == nil {
		return fail(fmt.Errorf("获取生产发布锁：释放函数为空"))
	}
	defer func() { _ = releaseLock() }()

	current, err := deployer.Store.LoadCurrent()
	if err != nil {
		return fail(fmt.Errorf("读取当前成功版本：%w", err))
	}
	target, err := deployer.resolveTarget(request, current)
	if err != nil {
		deployer.appendAudit(request, result, err)
		return fail(err)
	}
	result.SourceSHA = target.SourceSHA

	config := normalizeHostConfig(deployer.Config)
	if err := Preflight(ctx, config, deployer.Runner, deployer.Resources); err != nil {
		deployer.appendAudit(request, result, err)
		return fail(err)
	}
	if request.Operation == OperationDeploy {
		if err := deployer.pullAndVerify(ctx, target); err != nil {
			deployer.appendAudit(request, result, err)
			return fail(err)
		}
		if err := deployer.Store.SaveValidated(target); err != nil {
			deployer.appendAudit(request, result, err)
			return fail(fmt.Errorf("保存已验证候选：%w", err))
		}
	}

	override, err := RenderComposeOverride(target)
	if err != nil {
		deployer.appendAudit(request, result, err)
		return fail(err)
	}
	if err := writeFileAtomic(config.OverrideFile, override, 0o600); err != nil {
		deployer.appendAudit(request, result, err)
		return fail(fmt.Errorf("写入发布覆盖配置：%w", err))
	}

	if err := deployer.composeUpdate(ctx, config); err != nil {
		return deployer.failAfterUpdate(ctx, request, result, current, config, startedAt,
			fmt.Errorf("重建应用容器：%w", err))
	}
	if err := deployer.Health.Wait(ctx, releaseHealthTimeout, releaseHealthInterval); err != nil {
		return deployer.failAfterUpdate(ctx, request, result, current, config, startedAt,
			fmt.Errorf("新版本健康检查：%w", err))
	}

	target.SuccessfulAt = deployer.now()
	if err := deployer.Store.CommitSuccess(target); err != nil {
		return deployer.failAfterUpdate(ctx, request, result, current, config, startedAt,
			fmt.Errorf("提交成功发布状态：%w", err))
	}
	result.Status = "succeeded"
	result.RollbackStatus = "not-required"
	result.FinishedAt = deployer.now()
	if err := deployer.Store.AppendAudit(auditFrom(request, result)); err != nil {
		result.Status = "failed"
		result.DiagnosticID = makeDiagnosticID(startedAt, request.WorkflowRunID, request.TargetID)
		return result, fmt.Errorf("写入发布审计：%w", err)
	}
	return result, nil
}

func (deployer *Deployer) resolveTarget(request Request, current StoredRelease) (StoredRelease, error) {
	if request.Operation == OperationRollback {
		target, err := deployer.Store.LoadTarget(request.TargetID)
		if err != nil {
			return StoredRelease{}, fmt.Errorf("读取人工回滚目标：%w", err)
		}
		return target, nil
	}
	target, err := NewStoredRelease(*request.Manifest, deployer.Policy)
	if err != nil {
		return StoredRelease{}, fmt.Errorf("验证候选清单：%w", err)
	}
	if target.TargetID != request.TargetID {
		return StoredRelease{}, fmt.Errorf("%w：目标编号与候选运行编号不一致", ErrInvalidRequest)
	}
	if target.Compatibility != current.Compatibility {
		return StoredRelease{}, ErrIncompatibleRelease
	}
	return target, nil
}

func (deployer *Deployer) pullAndVerify(ctx context.Context, target StoredRelease) error {
	images := []string{target.Images.API, target.Images.Web, target.Images.Ops}
	for _, image := range images {
		if _, err := runSuccessful(ctx, deployer.Runner, "docker", []string{"pull", image}); err != nil {
			return fmt.Errorf("拉取镜像 %s：%w", imageNameOnly(image), err)
		}
	}
	for _, image := range images {
		result, err := runSuccessful(ctx, deployer.Runner, "docker", []string{
			"image", "inspect", "--format", "{{json .RepoDigests}}", image,
		})
		if err != nil {
			return fmt.Errorf("检查镜像摘要 %s：%w", imageNameOnly(image), err)
		}
		var digests []string
		if err := json.Unmarshal(bytes.TrimSpace(result.Stdout), &digests); err != nil {
			return fmt.Errorf("%w：%s 检查结果无效", ErrImageDigestMismatch, imageNameOnly(image))
		}
		matched := false
		for _, digest := range digests {
			if digest == image {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%w：%s", ErrImageDigestMismatch, imageNameOnly(image))
		}
	}
	return nil
}

func (deployer *Deployer) composeUpdate(ctx context.Context, config HostConfig) error {
	_, err := runSuccessful(ctx, deployer.Runner, "docker", append(composePrefix(config),
		"up", "-d", "--no-deps", "--no-build", "api", "scheduler", "web", "ops"))
	return err
}

func (deployer *Deployer) failAfterUpdate(
	ctx context.Context,
	request Request,
	result Result,
	previous StoredRelease,
	config HostConfig,
	startedAt time.Time,
	cause error,
) (Result, error) {
	result.Status = "failed"
	result.DiagnosticID = makeDiagnosticID(startedAt, request.WorkflowRunID, request.TargetID)
	deployer.collectDiagnostics(ctx, config, result.DiagnosticID)
	rollbackErr := deployer.restore(ctx, config, previous)
	if rollbackErr != nil {
		result.RollbackStatus = "failed"
	} else {
		result.RollbackStatus = "succeeded"
	}
	result.FinishedAt = deployer.now()
	deployer.appendAudit(request, result, cause)
	if rollbackErr != nil {
		return result, fmt.Errorf("%w；自动回滚失败：%v", cause, rollbackErr)
	}
	return result, cause
}

func (deployer *Deployer) restore(ctx context.Context, config HostConfig, previous StoredRelease) error {
	override, err := RenderComposeOverride(previous)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(config.OverrideFile, override, 0o600); err != nil {
		return err
	}
	if err := deployer.composeUpdate(ctx, config); err != nil {
		return err
	}
	return deployer.Health.Wait(ctx, releaseHealthTimeout, releaseHealthInterval)
}

func validateDeploymentRequest(request Request, policy ManifestPolicy) error {
	if request.Operation != OperationDeploy && request.Operation != OperationRollback {
		return fmt.Errorf("%w：操作类型无效", ErrInvalidRequest)
	}
	if request.Operation == OperationDeploy && !targetIDPattern.MatchString(request.TargetID) {
		return fmt.Errorf("%w：部署目标必须是候选运行编号", ErrInvalidRequest)
	}
	if request.Operation == OperationRollback && !validTargetID(request.TargetID) {
		return fmt.Errorf("%w：回滚目标无效", ErrInvalidRequest)
	}
	if !actorPattern.MatchString(request.Actor) {
		return fmt.Errorf("%w：GitHub 操作者无效", ErrInvalidRequest)
	}
	if request.WorkflowRunID <= 0 || !validWorkflowURL(request.WorkflowURL, request.WorkflowRunID, policy.Owner) {
		return fmt.Errorf("%w：GitHub 工作流链接无效", ErrInvalidRequest)
	}
	if request.Operation == OperationDeploy && request.Manifest == nil {
		return fmt.Errorf("%w：部署操作缺少候选清单", ErrInvalidRequest)
	}
	if request.Operation == OperationRollback && request.Manifest != nil {
		return fmt.Errorf("%w：人工回滚不得携带远程清单", ErrInvalidRequest)
	}
	return nil
}

func validWorkflowURL(value string, runID int64, owner string) bool {
	if !ownerPattern.MatchString(owner) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	return len(parts) == 5 && strings.EqualFold(parts[0], owner) && parts[1] == "konzhitai" &&
		parts[2] == "actions" && parts[3] == "runs" && parts[4] == strconv.FormatInt(runID, 10)
}

func (deployer *Deployer) now() time.Time {
	if deployer != nil && deployer.Now != nil {
		return deployer.Now().UTC()
	}
	return time.Now().UTC()
}

func makeDiagnosticID(startedAt time.Time, workflowRunID int64, targetID string) string {
	if !validTargetID(targetID) {
		targetID = "invalid"
	}
	if workflowRunID <= 0 {
		workflowRunID = 0
	}
	return startedAt.UTC().Format("20060102T150405.000000000Z") + "-" + strconv.FormatInt(workflowRunID, 10) + "-" + targetID
}

func auditFrom(request Request, result Result) AuditEvent {
	return AuditEvent{
		Operation: string(request.Operation), TargetID: request.TargetID, Status: result.Status,
		OccurredAt: result.FinishedAt, Actor: request.Actor, WorkflowRunID: request.WorkflowRunID,
		WorkflowURL: request.WorkflowURL, SourceSHA: result.SourceSHA,
		RollbackStatus: result.RollbackStatus, DiagnosticID: result.DiagnosticID,
	}
}

func (deployer *Deployer) appendAudit(request Request, result Result, _ error) {
	result.FinishedAt = deployer.now()
	if result.Status == "failed" && result.DiagnosticID == "" {
		result.DiagnosticID = makeDiagnosticID(result.StartedAt, request.WorkflowRunID, request.TargetID)
	}
	_ = deployer.Store.AppendAudit(auditFrom(request, result))
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("创建目标目录：%w", err)
	}
	temporary, err := os.CreateTemp(directory, ".release-override-")
	if err != nil {
		return fmt.Errorf("创建临时文件：%w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("覆盖配置目标不能是符号链接")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func (deployer *Deployer) collectDiagnostics(ctx context.Context, config HostConfig, diagnosticID string) {
	root := filepath.Join(deployer.Store.root, "diagnostics", diagnosticID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return
	}
	remaining := maxDiagnosticBytes
	commands := []struct {
		file     string
		args     []string
		maxLines int
	}{
		{file: "compose-ps.log", args: append(composePrefix(config), "ps", "--format", "json", "api", "scheduler", "web", "ops")},
		{file: "api.log", args: []string{"logs", "--tail", "200", "yunling-api-1"}, maxLines: 200},
		{file: "scheduler.log", args: []string{"logs", "--tail", "200", "yunling-scheduler-1"}, maxLines: 200},
		{file: "web.log", args: []string{"logs", "--tail", "200", "yunling-web-1"}, maxLines: 200},
		{file: "ops.log", args: []string{"logs", "--tail", "200", "yunling-ops-1"}, maxLines: 200},
	}
	for _, command := range commands {
		if remaining <= 0 {
			break
		}
		result, err := deployer.Runner.Run(ctx, "docker", command.args, nil)
		content := append(append([]byte(nil), result.Stdout...), result.Stderr...)
		if err != nil && len(content) == 0 {
			content = []byte("诊断命令执行失败\n")
		}
		content = redactDiagnostic(content, command.maxLines)
		if len(content) > remaining {
			content = content[:remaining]
		}
		if len(content) == 0 {
			content = []byte("无输出\n")
		}
		if len(content) > remaining {
			content = content[:remaining]
		}
		if err := os.WriteFile(filepath.Join(root, command.file), content, 0o600); err != nil {
			continue
		}
		remaining -= len(content)
	}
}

func redactDiagnostic(content []byte, maxLines int) []byte {
	var output bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 1024), maxDiagnosticBytes)
	lineCount := 0
	for scanner.Scan() {
		if maxLines > 0 && lineCount >= maxLines {
			break
		}
		line := scanner.Text()
		lower := strings.ToLower(line)
		secret := false
		for _, marker := range []string{
			"password", "secret", "token", "cookie", "authorization", "database_url", "private_key", "webhook",
		} {
			if strings.Contains(lower, marker) {
				secret = true
				break
			}
		}
		if secret || (strings.Contains(lower, "://") && strings.Contains(lower, "@")) {
			output.WriteString("[已脱敏]\n")
		} else {
			output.WriteString(line)
			output.WriteByte('\n')
		}
		lineCount++
	}
	return output.Bytes()
}

func imageNameOnly(image string) string {
	if index := strings.Index(image, "@sha256:"); index >= 0 {
		return image[:index]
	}
	return "镜像"
}
