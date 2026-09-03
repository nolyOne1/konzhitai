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
