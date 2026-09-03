package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"yunling.local/platform/internal/release"
)

const (
	trustedRepositoryID int64 = 1354623243
	trustedOwner              = "nolyone1"
	maxCLIInputBytes          = 256 << 10
	productionRoot            = "/opt/yunling"
)

type dependencies struct {
	bootstrap   func(context.Context) error
	execute     func(context.Context, release.Request) (release.Result, error)
	preflight   func(context.Context) error
	notify      func(context.Context, string, string, release.Result) error
	now         func() time.Time
	requireRoot bool
	isRoot      func() bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, realDependencies()))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, dependency dependencies) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return 2
	}
	switch args[0] {
	case "manifest":
		return runManifest(args[1:], stdout, stderr, dependency)
	case "request":
		return runRequest(args[1:], stdout, stderr)
	case "candidate":
		return runCandidate(args[1:], stderr)
	case "execute":
		return runExecute(args[1:], stdin, stdout, stderr, dependency)
	case "bootstrap":
		return runBootstrap(args[1:], stderr, dependency)
	case "preflight":
		return runPreflight(args[1:], stderr, dependency)
	case "notify":
		return runNotify(args[1:], stdin, stderr, dependency)
	case "help", "--help", "-h":
		writeUsage(stdout)
		return 0
	default:
		fmt.Fprintln(stderr, "未知子命令")
		writeUsage(stderr)
		return 2
	}
}

func runCandidate(args []string, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "authorize" {
		fmt.Fprintln(stderr, "candidate 只支持 authorize")
		return 2
	}
	flags := flag.NewFlagSet("candidate authorize", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputPath := flags.String("input", "", "GitHub workflow_run 元数据文件")
	repositoryID := flags.Int64("repository-id", trustedRepositoryID, "受信任仓库数字 ID")
	if err := flags.Parse(args[1:]); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 || *inputPath == "" || *repositoryID <= 0 {
		fmt.Fprintln(stderr, "candidate authorize 参数无效")
		return 2
	}
	body, err := readBoundedFile(*inputPath, maxCLIInputBytes)
	if err != nil {
		fmt.Fprintln(stderr, "候选来源元数据文件无效")
		return 1
	}
	run, err := release.DecodeRunMetadata(bytes.NewReader(body))
	if err != nil || release.ValidateCandidateRun(run, release.CandidatePolicy{RepositoryID: *repositoryID}) != nil {
		fmt.Fprintln(stderr, "候选来源运行不受信任")
		return 1
	}
	fmt.Fprintln(stderr, "候选来源运行已授权")
	return 0
}

func runBootstrap(args []string, stderr io.Writer, dependency dependencies) int {
	flags := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "bootstrap 不接受位置参数")
		return 2
	}
	if dependency.requireRoot && (dependency.isRoot == nil || !dependency.isRoot()) {
		fmt.Fprintln(stderr, "生产基线导入必须由 root 调用")
		return 1
	}
	if dependency.bootstrap == nil {
		fmt.Fprintln(stderr, "生产基线导入依赖不可用")
		return 1
	}
	if err := dependency.bootstrap(context.Background()); err != nil {
		fmt.Fprintln(stderr, "生产基线导入失败")
		return 1
	}
	fmt.Fprintln(stderr, "生产基线导入完成")
	return 0
}

func runExecute(args []string, stdin io.Reader, stdout, stderr io.Writer, dependency dependencies) int {
	flags := flag.NewFlagSet("execute", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "execute 不接受位置参数")
		return 2
	}
	if dependency.requireRoot && (dependency.isRoot == nil || !dependency.isRoot()) {
		fmt.Fprintln(stderr, "真实生产执行必须由 root 调用")
		return 1
	}
	if dependency.execute == nil {
		fmt.Fprintln(stderr, "生产执行依赖不可用")
		return 1
	}
	body, err := readBounded(stdin, maxCLIInputBytes)
	if err != nil {
		fmt.Fprintln(stderr, "生产请求超过 256 KiB 安全限制")
		return 1
	}
	request, err := release.DecodeRequest(bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(stderr, "生产请求不是严格 JSON")
		return 1
	}
	result, executeErr := dependency.execute(context.Background(), request)
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, "写入机器结果失败")
		return 1
	}
	if executeErr != nil {
		fmt.Fprintf(stderr, "生产操作失败，诊断编号：%s\n", safeDiagnosticID(result.DiagnosticID))
		return 1
	}
	return 0
}

func runPreflight(args []string, stderr io.Writer, dependency dependencies) int {
	flags := flag.NewFlagSet("preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 || dependency.preflight == nil {
		fmt.Fprintln(stderr, "生产预检参数或依赖无效")
		return 2
	}
	if err := dependency.preflight(context.Background()); err != nil {
		fmt.Fprintln(stderr, "生产预检失败")
		return 1
	}
	fmt.Fprintln(stderr, "生产预检通过")
	return 0
}

func runNotify(args []string, stdin io.Reader, stderr io.Writer, dependency dependencies) int {
	flags := flag.NewFlagSet("notify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 || dependency.notify == nil {
		fmt.Fprintln(stderr, "飞书通知参数或依赖无效")
		return 2
	}
	body, err := readBounded(stdin, maxCLIInputBytes)
	if err != nil {
		fmt.Fprintln(stderr, "发布结果超过 256 KiB 安全限制")
		return 1
	}
	result, err := release.DecodeResult(bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(stderr, "发布结果不是严格 JSON")
		return 1
	}
	webhook := os.Getenv("PRODUCTION_FEISHU_WEBHOOK")
	signingSecret := os.Getenv("PRODUCTION_FEISHU_SIGNING_SECRET")
	if err := dependency.notify(context.Background(), webhook, signingSecret, result); err != nil {
		fmt.Fprintln(stderr, "飞书发布通知失败")
		return 1
	}
	fmt.Fprintln(stderr, "飞书发布通知已发送")
	return 0
}

func runManifest(args []string, stdout, stderr io.Writer, dependency dependencies) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "manifest 需要 create 或 validate")
		return 2
	}
	switch args[0] {
	case "create":
		return createManifest(args[1:], stderr, dependency)
	case "validate":
		return validateManifest(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "manifest 只支持 create 或 validate")
		return 2
	}
}

func createManifest(args []string, stderr io.Writer, dependency dependencies) int {
	flags := flag.NewFlagSet("manifest create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	candidateID := flags.Int64("candidate-run-id", 0, "候选 GitHub 运行编号")
	repositoryID := flags.Int64("repository-id", 0, "GitHub 仓库数字 ID")
	sourceSHA := flags.String("source-sha", "", "源提交 SHA")
	services := flags.String("services", "", "services 镜像摘要")
	web := flags.String("web", "", "web 镜像摘要")
	ops := flags.String("ops", "", "ops 镜像摘要")
	repositoryRoot := flags.String("repository-root", "", "仓库根目录")
	agentLockPath := flags.String("agent-lock", "", "代理发布锁文件")
	outputPath := flags.String("output", "", "输出清单路径")
	owner := flags.String("owner", trustedOwner, "GHCR 所有者")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 || *repositoryRoot == "" || *agentLockPath == "" || *outputPath == "" {
		fmt.Fprintln(stderr, "manifest create 缺少必要参数")
		return 2
	}
	root, err := filepath.Abs(*repositoryRoot)
	if err != nil {
		fmt.Fprintln(stderr, "仓库根目录无效")
		return 1
	}
	lockPath := *agentLockPath
	if !filepath.IsAbs(lockPath) {
		lockPath = filepath.Join(root, filepath.FromSlash(lockPath))
	}
	lock, err := release.LoadAgentLock(lockPath)
	if err != nil {
		fmt.Fprintln(stderr, "代理发布锁无效")
		return 1
	}
	migrationDigest, err := release.MigrationTreeDigest(filepath.Join(root, "migrations"))
	if err != nil {
		fmt.Fprintln(stderr, "迁移树摘要计算失败")
		return 1
	}
	contractDigest, err := deploymentContractAt(root)
	if err != nil {
		fmt.Fprintln(stderr, "部署契约摘要计算失败")
		return 1
	}
	now := time.Now
	if dependency.now != nil {
		now = dependency.now
	}
	manifest := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion, CandidateRunID: *candidateID,
		RepositoryID: *repositoryID, SourceSHA: *sourceSHA, CreatedAt: now().UTC(),
		Images: release.Images{Services: *services, Web: *web, Ops: *ops},
		Compatibility: release.Compatibility{
			MigrationTreeSHA256: migrationDigest, DeploymentContractSHA256: contractDigest,
			AgentVersion: lock.Version, AgentManifestSHA256: lock.ManifestSHA256,
		},
	}
	if err := release.ValidateManifest(manifest, release.ManifestPolicy{RepositoryID: *repositoryID, Owner: *owner}); err != nil {
		fmt.Fprintln(stderr, "生成的候选清单未通过安全校验")
		return 1
	}
	if err := writePrivateJSON(*outputPath, manifest); err != nil {
		fmt.Fprintln(stderr, "写入候选清单失败")
		return 1
	}
	fmt.Fprintln(stderr, "候选清单已生成")
	return 0
}

func validateManifest(args []string, _ io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("manifest validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputPath := flags.String("input", "", "候选清单路径")
	repositoryID := flags.Int64("repository-id", trustedRepositoryID, "受信任仓库数字 ID")
	owner := flags.String("owner", trustedOwner, "受信任 GHCR 所有者")
	if err := flags.Parse(args); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 || *inputPath == "" {
		fmt.Fprintln(stderr, "manifest validate 缺少 --input")
		return 2
	}
	manifest, err := loadManifest(*inputPath)
	if err != nil || release.ValidateManifest(manifest, release.ManifestPolicy{RepositoryID: *repositoryID, Owner: *owner}) != nil {
		fmt.Fprintln(stderr, "候选清单无效")
		return 1
	}
	fmt.Fprintln(stderr, "候选清单有效")
	return 0
}

func runRequest(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(stderr, "request 只支持 create")
		return 2
	}
	flags := flag.NewFlagSet("request create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	operation := flags.String("operation", "", "deploy 或 rollback")
	targetID := flags.String("target-id", "", "候选运行编号或 bootstrap")
	actor := flags.String("actor", "", "GitHub 操作者")
	workflowRunID := flags.Int64("workflow-run-id", 0, "生产工作流运行编号")
	workflowURL := flags.String("workflow-url", "", "生产工作流链接")
	manifestPath := flags.String("manifest", "", "部署候选清单路径")
	if err := flags.Parse(args[1:]); err != nil {
		return flagExitCode(err)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "request create 不接受位置参数")
		return 2
	}
	request := release.Request{
		Operation: release.Operation(*operation), TargetID: *targetID, Actor: *actor,
		WorkflowRunID: *workflowRunID, WorkflowURL: *workflowURL,
	}
	if *manifestPath != "" {
		manifest, err := loadManifest(*manifestPath)
		if err != nil {
			fmt.Fprintln(stderr, "请求中的候选清单无效")
			return 1
		}
		request.Manifest = &manifest
	}
	policy := release.ManifestPolicy{RepositoryID: trustedRepositoryID, Owner: trustedOwner}
	if err := release.ValidateRequest(request, policy); err != nil {
		fmt.Fprintln(stderr, "生产请求参数无效")
		return 1
	}
	if request.Manifest != nil && release.ValidateManifest(*request.Manifest, policy) != nil {
		fmt.Fprintln(stderr, "请求中的候选清单未通过来源校验")
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(request); err != nil {
		fmt.Fprintln(stderr, "写入生产请求失败")
		return 1
	}
	return 0
}

func realDependencies() dependencies {
	config := release.HostConfig{
		RootDir: productionRoot, ComposeFile: productionRoot + "/deploy/docker-compose.yml",
		OverrideFile: productionRoot + "/deploy/docker-compose.release.yml",
		EnvFile:      productionRoot + "/deploy/.env", ProjectName: "yunling",
		PublicBaseURL: "https://aiwise.top",
	}
	runner := release.NewCommandRunner()
	resources := release.NewResourceReader()
	locker := release.NewLocker()
	health, healthErr := release.NewDockerHealthChecker(config, runner, nil)
	store := release.NewStateStore(productionRoot + "/releases")
	policy := release.ManifestPolicy{RepositoryID: trustedRepositoryID, Owner: trustedOwner}
	deployer := &release.Deployer{
		Config: config, Policy: policy, Store: store, Runner: runner,
		Resources: resources, Locker: locker, Health: health, Now: time.Now,
	}
	bootstrapper := &release.Bootstrapper{
		RootDir: productionRoot, ComposeFile: config.ComposeFile, OverrideFile: config.OverrideFile,
		AgentLockPath: productionRoot + "/deploy/agent/release-lock.json", Store: store,
		Host:   release.NewDockerBootstrapHost(runner, "yunling-api-1", "yunling_agent_releases"),
		Locker: locker, Now: time.Now,
	}
	notifier := release.NewNotifier(http.DefaultClient, time.Now)
	return dependencies{
		requireRoot: true, isRoot: currentUserIsRoot, now: time.Now,
		bootstrap: bootstrapper.Run,
		execute: func(ctx context.Context, request release.Request) (release.Result, error) {
			if healthErr != nil {
				return release.Result{}, healthErr
			}
			return deployer.Execute(ctx, request)
		},
		preflight: func(ctx context.Context) error {
			return release.Preflight(ctx, config, runner, resources)
		},
		notify: notifier.Send,
	}
}

func deploymentContractAt(root string) (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if err := os.Chdir(root); err != nil {
		return "", err
	}
	defer func() { _ = os.Chdir(current) }()
	return release.DeploymentContractDigest([]string{"deploy/docker-compose.yml"})
}

func loadManifest(path string) (release.Manifest, error) {
	body, err := readBoundedFile(path, 1<<20)
	if err != nil {
		return release.Manifest{}, err
	}
	return release.DecodeManifest(bytes.NewReader(body))
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("输入文件无效")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readBounded(file, limit)
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil || limit <= 0 {
		return nil, errors.New("输入无效")
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("输入超过限制")
	}
	return body, nil
}

func writePrivateJSON(path string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".manifest-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
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
	return os.Rename(temporaryPath, path)
}

func safeDiagnosticID(value string) string {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') && character != '-' && character != '.' {
			return "不可用"
		}
	}
	if value == "" || len(value) > 128 {
		return "不可用"
	}
	return value
}

func flagExitCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func writeUsage(writer io.Writer) {
	fmt.Fprintln(writer, "用法：yunling-release <manifest|candidate|request|execute|bootstrap|preflight|notify> [参数]")
}
