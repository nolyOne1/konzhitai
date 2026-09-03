//go:build releaseintegration

package releaseintegration_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/release"
	"yunling.local/platform/internal/testpostgres"
)

const (
	testNginxImage  = "nginx:1.29.8-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de"
	testAlpineImage = "alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b"
)

var pushedDigestPattern = regexp.MustCompile(`(?m)digest:\s*(sha256:[0-9a-f]{64})`)

type releaseHarness struct {
	project         string
	composeFile     string
	envFile         string
	overrideFile    string
	registry        string
	store           *release.StateStore
	deployer        release.Deployer
	health          *composeHealthChecker
	good            release.Manifest
	bad             release.Manifest
	initialAppIDs   map[string]string
	goodImageIDs    map[string]string
	temporaryImages []string
}

func TestRealComposeDeployAndRollbackPreserveInfrastructure(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("真实发布演练需要 Docker")
	}
	project := "yunling-release-test-" + strconv.Itoa(os.Getpid())
	harness := startHarness(t, project)
	beforeInfrastructure := harness.containerIDs(t, []string{"postgres", "redis", "minio", "caddy"})
	beforeVolumes := harness.namedVolumes(t)

	harness.deployGoodRelease(t)
	harness.deployBadWebReleaseAndRequireRollback(t)

	afterInfrastructure := harness.containerIDs(t, []string{"postgres", "redis", "minio", "caddy"})
	if !reflect.DeepEqual(beforeInfrastructure, afterInfrastructure) {
		t.Fatalf("基础设施容器被替换：%v -> %v", beforeInfrastructure, afterInfrastructure)
	}
	if afterVolumes := harness.namedVolumes(t); !reflect.DeepEqual(beforeVolumes, afterVolumes) {
		t.Fatalf("命名卷被替换：%v -> %v", beforeVolumes, afterVolumes)
	}
}

func startHarness(t *testing.T, project string) *releaseHarness {
	t.Helper()
	if !regexp.MustCompile(`^yunling-release-test-[1-9][0-9]*$`).MatchString(project) {
		t.Fatalf("演练项目名无效：%s", project)
	}
	repositoryRoot := testpostgres.RepositoryRoot(t)
	root := t.TempDir()
	harness := &releaseHarness{
		project:      project,
		composeFile:  filepath.Join(repositoryRoot, "deploy", "release", "testdata", "docker-compose.yml"),
		envFile:      filepath.Join(root, "compose.env"),
		overrideFile: filepath.Join(root, "docker-compose.release.yml"),
	}
	if err := os.WriteFile(harness.envFile, []byte("COMPOSE_ANSI=never\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	harness.docker(t, "version")
	harness.docker(t, "compose", "version")
	harness.requireUnusedProject(t)
	bootstrapImages := release.ServiceImages{
		API:       harness.bootstrapImage("api"),
		Scheduler: harness.bootstrapImage("scheduler"),
		Web:       harness.bootstrapImage("web"),
		Ops:       harness.bootstrapImage("ops"),
	}
	for _, image := range []string{bootstrapImages.API, bootstrapImages.Scheduler, bootstrapImages.Web, bootstrapImages.Ops} {
		if output, err := dockerOutput(context.Background(), "image", "inspect", image); err == nil {
			t.Fatalf("演练基线镜像标签已存在，拒绝覆盖：%s\n%s", image, output)
		}
		harness.temporaryImages = append(harness.temporaryImages, image)
	}
	t.Cleanup(func() { harness.cleanup(t) })

	harness.docker(t, "pull", testNginxImage)
	harness.docker(t, "pull", testAlpineImage)
	for _, image := range []string{bootstrapImages.API, bootstrapImages.Scheduler, bootstrapImages.Web, bootstrapImages.Ops} {
		harness.docker(t, "tag", testNginxImage, image)
	}

	compatibility := release.Compatibility{
		MigrationTreeSHA256:      strings.Repeat("a", 64),
		DeploymentContractSHA256: strings.Repeat("b", 64),
		AgentVersion:             "0.1.0",
		AgentManifestSHA256:      strings.Repeat("c", 64),
	}
	harness.store = release.NewStateStore(filepath.Join(root, "releases"))
	bootstrap, err := harness.store.CreateBootstrap(bootstrapImages, compatibility, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	override, err := release.RenderComposeOverride(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harness.overrideFile, override, 0o600); err != nil {
		t.Fatal(err)
	}

	harness.compose(t, "up", "-d", "--wait", "registry", "postgres", "redis", "minio", "caddy")
	harness.registry = "127.0.0.1:" + harness.publishedPort(t, "registry", "5000")
	servicesImage := harness.pushImage(t, testNginxImage, "yunling-services", "good")
	goodWebImage := harness.pushImage(t, testNginxImage, "yunling-web", "good")
	opsImage := harness.pushImage(t, testNginxImage, "yunling-ops", "good")
	badWebImage := harness.pushImage(t, testAlpineImage, "yunling-web", "bad")

	harness.good = testManifest(101, "d", release.Images{Services: servicesImage, Web: goodWebImage, Ops: opsImage}, compatibility)
	harness.bad = testManifest(102, "e", release.Images{Services: servicesImage, Web: badWebImage, Ops: opsImage}, compatibility)
	goodPolicy, err := release.NewReleaseIntegrationPolicy(42, "nolyone1", harness.good.Images)
	if err != nil {
		t.Fatal(err)
	}

	harness.compose(t, "up", "-d", "--no-deps", "api", "scheduler", "web", "ops")
	webURL := "http://127.0.0.1:" + harness.publishedPort(t, "web", "8080")
	harness.health = &composeHealthChecker{harness: harness, webURL: webURL, client: &http.Client{Timeout: 2 * time.Second}}
	if err := harness.health.Wait(context.Background(), 30*time.Second, 250*time.Millisecond); err != nil {
		t.Fatalf("启动基线应用：%v", err)
	}
	harness.initialAppIDs = harness.containerIDs(t, []string{"api", "scheduler", "web", "ops"})

	config := release.HostConfig{
		RootDir: root, ComposeFile: harness.composeFile, OverrideFile: harness.overrideFile,
		EnvFile: harness.envFile, ProjectName: project, MinFreeBytes: 1, MinMemory: 1,
	}
	harness.deployer = release.Deployer{
		Config: config, Policy: goodPolicy, Store: harness.store,
		Runner: release.NewCommandRunner(), Resources: release.NewResourceReader(), Locker: release.NewLocker(),
		Health: harness.health, HealthTimeout: 8 * time.Second, HealthInterval: 250 * time.Millisecond,
	}
	return harness
}

func testManifest(candidateID int64, shaByte string, images release.Images, compatibility release.Compatibility) release.Manifest {
	return release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion, CandidateRunID: candidateID, RepositoryID: 42,
		SourceSHA: strings.Repeat(shaByte, 40), CreatedAt: time.Now().UTC(), Images: images, Compatibility: compatibility,
	}
}

func (harness *releaseHarness) deployGoodRelease(t *testing.T) {
	t.Helper()
	beforeInternal, beforePublic := harness.health.internalChecks, harness.health.publicChecks
	result, err := harness.deployer.Execute(context.Background(), deployRequest(harness.good, 9001))
	if err != nil {
		t.Fatalf("真实成功发布：%v", err)
	}
	if result.Status != "succeeded" || result.RollbackStatus != "not-required" {
		t.Fatalf("成功发布结果无效：%+v", result)
	}
	current, err := harness.store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	previous, err := harness.store.LoadPrevious()
	if err != nil {
		t.Fatal(err)
	}
	if current.TargetID != "101" || previous.TargetID != "bootstrap" {
		t.Fatalf("发布状态错误：current=%s previous=%s", current.TargetID, previous.TargetID)
	}
	after := harness.containerIDs(t, []string{"api", "scheduler", "web", "ops"})
	for service, before := range harness.initialAppIDs {
		if after[service] == before {
			t.Fatalf("应用容器 %s 未在成功发布时替换", service)
		}
	}
	if harness.health.internalChecks <= beforeInternal || harness.health.publicChecks <= beforePublic {
		t.Fatalf("未执行内部和公开探测：internal=%d public=%d", harness.health.internalChecks, harness.health.publicChecks)
	}
	harness.goodImageIDs = harness.applicationImageIDs(t)
}

func (harness *releaseHarness) deployBadWebReleaseAndRequireRollback(t *testing.T) {
	t.Helper()
	badPolicy, err := release.NewReleaseIntegrationPolicy(42, "nolyone1", harness.bad.Images)
	if err != nil {
		t.Fatal(err)
	}
	harness.deployer.Policy = badPolicy
	result, err := harness.deployer.Execute(context.Background(), deployRequest(harness.bad, 9002))
	if err == nil {
		t.Fatal("故障 Web 候选必须触发发布失败")
	}
	if result.Status != "failed" || result.RollbackStatus != "succeeded" || result.DiagnosticID == "" {
		t.Fatalf("自动回滚结果无效：%+v", result)
	}
	current, loadErr := harness.store.LoadCurrent()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if current.TargetID != "101" {
		t.Fatalf("故障候选改变了当前成功版本：%s", current.TargetID)
	}
	if got := harness.applicationImageIDs(t); !reflect.DeepEqual(got, harness.goodImageIDs) {
		t.Fatalf("回滚后应用镜像未恢复：%v -> %v", harness.goodImageIDs, got)
	}
}

func deployRequest(manifest release.Manifest, workflowRunID int64) release.Request {
	return release.Request{
		Operation: release.OperationDeploy, TargetID: strconv.FormatInt(manifest.CandidateRunID, 10),
		Actor: "nolyOne1", WorkflowRunID: workflowRunID,
		WorkflowURL: "https://github.com/nolyOne1/konzhitai/actions/runs/" + strconv.FormatInt(workflowRunID, 10),
		Manifest:    &manifest,
	}
}

func (harness *releaseHarness) pushImage(t *testing.T, source, repository, tag string) string {
	t.Helper()
	target := harness.registry + "/" + repository + ":" + tag
	if output, err := dockerOutput(context.Background(), "image", "inspect", target); err == nil {
		t.Fatalf("演练仓库镜像标签已存在，拒绝覆盖：%s\n%s", target, output)
	}
	harness.temporaryImages = append(harness.temporaryImages, target)
	harness.docker(t, "tag", source, target)
	output := harness.docker(t, "push", target)
	match := pushedDigestPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		t.Fatalf("无法从镜像推送结果读取摘要：%s", output)
	}
	digestReference := harness.registry + "/" + repository + "@" + match[1]
	harness.temporaryImages = append(harness.temporaryImages, digestReference)
	harness.docker(t, "pull", digestReference)
	return digestReference
}

func (harness *releaseHarness) publishedPort(t *testing.T, service, port string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		output, err := harness.composeResult(context.Background(), "port", service, port)
		if err == nil {
			address := strings.TrimSpace(output)
			if index := strings.LastIndex(address, ":"); index >= 0 && index+1 < len(address) {
				if number, parseErr := strconv.Atoi(address[index+1:]); parseErr == nil && number > 0 && number <= 65535 {
					return strconv.Itoa(number)
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("无法读取 %s 的随机本机端口", service)
	return ""
}

func (harness *releaseHarness) containerIDs(t *testing.T, services []string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(services))
	for _, service := range services {
		output := strings.TrimSpace(harness.compose(t, "ps", "-q", service))
		if !regexp.MustCompile(`^[0-9a-f]{12,64}$`).MatchString(output) {
			t.Fatalf("服务 %s 容器编号无效：%q", service, output)
		}
		result[service] = output
	}
	return result
}

func (harness *releaseHarness) applicationImageIDs(t *testing.T) map[string]string {
	t.Helper()
	containers := harness.containerIDs(t, []string{"api", "scheduler", "web", "ops"})
	images := make(map[string]string, len(containers))
	for service, container := range containers {
		images[service] = strings.TrimSpace(harness.docker(t, "inspect", "--format", "{{.Image}}", container))
	}
	return images
}

func (harness *releaseHarness) namedVolumes(t *testing.T) []string {
	t.Helper()
	output := harness.docker(t, "volume", "ls", "--filter", "label=com.docker.compose.project="+harness.project, "--format", "{{.Name}}")
	var volumes []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if value := strings.TrimSpace(line); value != "" {
			volumes = append(volumes, value)
		}
	}
	sort.Strings(volumes)
	if len(volumes) != 5 {
		t.Fatalf("演练命名卷数量错误：%v", volumes)
	}
	return volumes
}

func (harness *releaseHarness) compose(t *testing.T, arguments ...string) string {
	t.Helper()
	output, err := harness.composeResult(context.Background(), arguments...)
	if err != nil {
		t.Fatalf("docker compose %s：%v\n%s", strings.Join(arguments, " "), err, output)
	}
	return output
}

func (harness *releaseHarness) composeResult(ctx context.Context, arguments ...string) (string, error) {
	base := []string{"compose", "--project-name", harness.project, "--env-file", harness.envFile, "-f", harness.composeFile, "-f", harness.overrideFile}
	return dockerOutput(ctx, append(base, arguments...)...)
}

func (harness *releaseHarness) docker(t *testing.T, arguments ...string) string {
	t.Helper()
	output, err := dockerOutput(context.Background(), arguments...)
	if err != nil {
		t.Fatalf("docker %s：%v\n%s", strings.Join(arguments, " "), err, output)
	}
	return output
}

func dockerOutput(parent context.Context, arguments ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", arguments...)
	command.Env = append(os.Environ(), "COMPOSE_ANSI=never")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), ctx.Err()
	}
	return string(output), err
}

func (harness *releaseHarness) cleanup(t *testing.T) {
	t.Helper()
	if !regexp.MustCompile(`^yunling-release-test-[1-9][0-9]*$`).MatchString(harness.project) {
		t.Errorf("拒绝清理非演练项目：%s", harness.project)
		return
	}
	if output, err := harness.composeResult(context.Background(), "down", "-v", "--remove-orphans"); err != nil {
		t.Logf("清理演练项目失败：%v\n%s", err, output)
	}
	for _, image := range harness.temporaryImages {
		if !safeTemporaryImage(harness.project, harness.registry, image) {
			t.Errorf("拒绝清理非演练镜像：%s", image)
			continue
		}
		_, _ = dockerOutput(context.Background(), "image", "rm", image)
	}
}

func (harness *releaseHarness) requireUnusedProject(t *testing.T) {
	t.Helper()
	filter := "label=com.docker.compose.project=" + harness.project
	for _, arguments := range [][]string{
		{"ps", "-aq", "--filter", filter},
		{"volume", "ls", "-q", "--filter", filter},
		{"network", "ls", "-q", "--filter", filter},
	} {
		if output := strings.TrimSpace(harness.docker(t, arguments...)); output != "" {
			t.Fatalf("演练项目名与已有 Docker 资源冲突：%s", output)
		}
	}
}

func (harness *releaseHarness) bootstrapImage(service string) string {
	digest := sha256.Sum256([]byte(harness.project + ":" + service))
	return fmt.Sprintf("yunling-local-bootstrap/%s:%x", service, digest[:6])
}

func safeTemporaryImage(project, registry, image string) bool {
	if strings.HasPrefix(image, "yunling-local-bootstrap/") {
		for _, service := range []string{"api", "scheduler", "web", "ops"} {
			digest := sha256.Sum256([]byte(project + ":" + service))
			if image == fmt.Sprintf("yunling-local-bootstrap/%s:%x", service, digest[:6]) {
				return true
			}
		}
		return false
	}
	if registry == "" {
		return false
	}
	pattern := `^` + regexp.QuoteMeta(registry) + `/yunling-(services|web|ops)(:(good|bad)|@sha256:[0-9a-f]{64})$`
	return regexp.MustCompile(pattern).MatchString(image)
}

type composeHealthChecker struct {
	harness        *releaseHarness
	webURL         string
	client         *http.Client
	internalChecks int
	publicChecks   int
}

func (checker *composeHealthChecker) CheckOnce(ctx context.Context) error {
	if checker == nil || checker.harness == nil || checker.client == nil || ctx == nil {
		return release.ErrHealthCheckFailed
	}
	for _, check := range []struct {
		service string
		port    string
		path    string
	}{
		{service: "api", port: "8080", path: "/api/health"},
		{service: "scheduler", port: "8080", path: "/healthz"},
		{service: "web", port: "8080", path: "/healthz"},
		{service: "ops", port: "8081", path: "/healthz"},
	} {
		container, err := checker.harness.containerIDContext(ctx, check.service)
		if err != nil {
			return err
		}
		status, err := dockerOutput(ctx, "inspect", "--format", "{{json .State.Health.Status}}", container)
		if err != nil || strings.TrimSpace(status) != `"healthy"` {
			return fmt.Errorf("%w：%s 未就绪", release.ErrHealthCheckFailed, check.service)
		}
		if _, err := dockerOutput(ctx, "exec", container, "wget", "-qO-", "--timeout=2", "http://127.0.0.1:"+check.port+check.path); err != nil {
			return fmt.Errorf("%w：%s 内部探测失败", release.ErrHealthCheckFailed, check.service)
		}
		checker.internalChecks++
	}
	for _, path := range []string{"/healthz", "/api/health"} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, checker.webURL+path, nil)
		if err != nil {
			return err
		}
		response, err := checker.client.Do(request)
		if err != nil {
			return fmt.Errorf("%w：本机公开探测失败", release.ErrHealthCheckFailed)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || len(body) == 0 {
			return fmt.Errorf("%w：本机公开探测响应无效", release.ErrHealthCheckFailed)
		}
		checker.publicChecks++
	}
	return nil
}

func (checker *composeHealthChecker) Wait(ctx context.Context, timeout, interval time.Duration) error {
	if ctx == nil || timeout <= 0 || interval <= 0 {
		return release.ErrHealthCheckFailed
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for {
		if err := checker.CheckOnce(deadline); err == nil {
			return nil
		} else {
			lastErr = err
		}
		timer := time.NewTimer(interval)
		select {
		case <-deadline.Done():
			timer.Stop()
			return fmt.Errorf("%w：%v", release.ErrHealthCheckFailed, lastErr)
		case <-timer.C:
		}
	}
}

func (harness *releaseHarness) containerIDContext(ctx context.Context, service string) (string, error) {
	output, err := harness.composeResult(ctx, "ps", "-q", service)
	if err != nil {
		return "", err
	}
	container := strings.TrimSpace(output)
	if !regexp.MustCompile(`^[0-9a-f]{12,64}$`).MatchString(container) {
		return "", fmt.Errorf("%w：%s 容器不存在", release.ErrHealthCheckFailed, service)
	}
	return container, nil
}
