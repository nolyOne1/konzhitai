package integration_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"yunling.local/platform/internal/testpostgres"
)

var (
	workflowActionPattern = regexp.MustCompile(`(?m)^\s*uses:\s*[^@\s]+@([^\s#]+)`)
	fullCommitSHA         = regexp.MustCompile(`^[0-9a-f]{40}$`)
	writePermission       = regexp.MustCompile(`(?m)^\s*[a-z-]+:\s*write\s*$`)
	nextWorkflowJob       = regexp.MustCompile(`(?m)^  [a-z][a-z0-9_-]*:\n`)
)

func readRepoText(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(testpostgres.RepositoryRoot(t), filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("读取 %s：%v", name, err)
	}
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func ciJob(t *testing.T, workflow, jobID string) string {
	t.Helper()
	marker := "  " + jobID + ":\n"
	start := strings.Index(workflow, marker)
	if start < 0 {
		t.Fatalf("CI 工作流缺少 Job %q", jobID)
	}
	tail := workflow[start+len(marker):]
	if next := nextWorkflowJob.FindStringIndex(tail); next != nil {
		tail = tail[:next[0]]
	}
	return tail
}

func requireCIText(t *testing.T, content string, required ...string) {
	t.Helper()
	for _, text := range required {
		if !strings.Contains(content, text) {
			t.Errorf("CI 契约缺少 %q", text)
		}
	}
}

func TestCIWorkflowUsesSafeTriggersPermissionsAndPins(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	requireCIText(t, workflow,
		"name: 云令 CI",
		"pull_request:\n    branches:\n      - main",
		"push:\n    branches:\n      - main",
		"workflow_dispatch:",
		"permissions:\n  contents: read",
		"group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}",
		"cancel-in-progress: true",
	)
	for _, forbidden := range []string{
		"pull_request_target",
		"${{ secrets.",
		"id-token:",
		"packages:",
		"deployments:",
		"actions: write",
		"aiwise.top",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("CI 工作流包含禁止内容 %q", forbidden)
		}
	}
	if writePermission.MatchString(workflow) {
		t.Error("CI 工作流不得申请写权限")
	}
	refs := workflowActionPattern.FindAllStringSubmatch(workflow, -1)
	if len(refs) == 0 {
		t.Fatal("CI 工作流至少应使用检出 Action")
	}
	for _, ref := range refs {
		if !fullCommitSHA.MatchString(ref[1]) {
			t.Errorf("Action 未固定到完整提交 SHA：%s", ref[1])
		}
	}
}

func TestCIWorkflowDefinesStableFiveJobs(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	checks := map[string]string{
		"backend":    "后端测试与构建",
		"web":        "前端测试与构建",
		"e2e":        "端到端测试",
		"agent":      "代理安装与打包",
		"deployment": "部署配置与镜像",
	}
	for jobID, displayName := range checks {
		job := ciJob(t, workflow, jobID)
		requireCIText(t, job, "name: "+displayName, "runs-on: ubuntu-latest")
		if strings.Contains(job, "needs:") {
			t.Errorf("Job %s 不得依赖其他质量门禁 Job", jobID)
		}
	}
}

func TestCIConfigFilesAreForcedToLF(t *testing.T) {
	attributes := readRepoText(t, ".gitattributes")
	requireCIText(t, attributes,
		"*.sh text eol=lf",
		"*.service text eol=lf",
		"Makefile text eol=lf",
		"Dockerfile* text eol=lf",
		"*.yml text eol=lf",
		"*.yaml text eol=lf",
	)
}

func TestCIBackendAndWebJobsRunCompleteGates(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	backend := ciJob(t, workflow, "backend")
	requireCIText(t, backend,
		"uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6",
		"go-version-file: go.mod",
		"cache-dependency-path: go.sum",
		"go test -race -p=1 ./... -count=1",
		"./cmd/api",
		"./cmd/scheduler",
		"./cmd/ops",
		"./cmd/agent",
		"./cmd/bootstrap",
		"CGO_ENABLED: \"0\"",
	)

	web := ciJob(t, workflow, "web")
	requireCIText(t, web,
		"uses: actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5",
		"node-version: \"24\"",
		"cache: npm",
		"cache-dependency-path: package-lock.json",
		"npm ci",
		"npm run test:web",
		"npm run build:web",
	)
}

func TestCIEndToEndJobRunsLocallyAndUploadsOnlyFailureDiagnostics(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	e2e := ciJob(t, workflow, "e2e")
	requireCIText(t, e2e,
		"uses: actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444 # v5",
		"node-version: \"24\"",
		"npm ci",
		"npx playwright install --with-deps chromium",
		"npm run test:e2e",
		"if: ${{ failure() || cancelled() }}",
		"uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4",
		"name: playwright-failure-diagnostics",
		"apps/web/playwright-report",
		"apps/web/test-results",
		"retention-days: 7",
	)
	if strings.Contains(e2e, "YUNLING_E2E_BASE_URL") {
		t.Error("CI E2E 必须由 Playwright 启动本地 Vite，不得指定外部地址")
	}
	playwrightConfig := readRepoText(t, "apps/web/playwright.config.ts")
	requireCIText(t, playwrightConfig,
		"['github']",
		"['html', { outputFolder: 'playwright-report', open: 'never' }]",
		"screenshot: 'only-on-failure'",
		"trace: 'retain-on-failure'",
	)
}

func TestCIAgentJobTestsAndPackagesBothArchitectures(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	agent := ciJob(t, workflow, "agent")
	requireCIText(t, agent,
		"uses: actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16 # v6",
		"go-version-file: go.mod",
		"bash -n deploy/agent/*.sh",
		"bash deploy/agent/install_test.sh",
		"sh deploy/agent/package_test.sh",
		"GOARCH=amd64",
		"GOARCH=arm64",
		"sh deploy/agent/package.sh",
		"manifest.json",
		"sha256sum",
		"byte_size",
	)
	if strings.Contains(agent, "actions/upload-artifact") {
		t.Error("代理发布包只能在 Runner 临时验证，不得上传")
	}
}

func TestCIDeploymentJobValidatesWithoutStartingOrDeploying(t *testing.T) {
	workflow := readRepoText(t, ".github/workflows/ci.yml")
	deployment := ciJob(t, workflow, "deployment")
	requireCIText(t, deployment,
		"git ls-files -z -- '*.sh' '*.service' 'Makefile' 'Dockerfile*' '*.yml' '*.yaml'",
		"文件必须使用 LF 换行",
		"docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet",
		"docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml build web api scheduler ops bootstrap",
	)
	for _, forbidden := range []string{
		"docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml up",
		"docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml run",
		"build web api scheduler ops bootstrap minio",
		"docker push",
		"ssh ",
	} {
		if strings.Contains(deployment, forbidden) {
			t.Errorf("部署验证 Job 包含越界命令 %q", forbidden)
		}
	}
}
