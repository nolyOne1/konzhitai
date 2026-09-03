package release

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const validCandidateWorkflowFixture = `name: 云令候选版本
"on":
  workflow_run:
    workflows: ["云令 CI"]
    types: [completed]
permissions:
  contents: read
concurrency:
  group: candidate-${{ github.event.workflow_run.head_sha }}
  cancel-in-progress: true
jobs:
  publish:
    if: github.event.workflow_run.conclusion == 'success' && github.event.workflow_run.head_branch == 'main' && github.event.workflow_run.event == 'push' && github.event.workflow_run.repository.id == github.repository_id
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
      id-token: write
      attestations: write
    steps:
      - name: 检出精确提交
        uses: actions/checkout@fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09
        with:
          ref: ${{ github.event.workflow_run.head_sha }}
      - name: 授权候选来源
        run: |
          umask 077
          jq -c '.workflow_run' "$GITHUB_EVENT_PATH" > "$RUNNER_TEMP/workflow-run.json"
          test "$(wc -c < "$RUNNER_TEMP/workflow-run.json")" -le 262144
          yunling-release candidate authorize --input "$RUNNER_TEMP/workflow-run.json" --repository-id "${{ github.repository_id }}"
      - name: 登录 GHCR
        uses: docker/login-action@f4ef78c080cd8ba55a85445d5b36e214a81df20a
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - id: build-services
        name: 构建 services 镜像
        uses: docker/build-push-action@3b5e8027fcad23fda98b2e3ac259d8d67585f671
        with:
          file: deploy/Dockerfile.services
          push: true
          platforms: linux/amd64
          tags: ghcr.io/nolyone1/yunling-services:sha-${{ github.event.workflow_run.head_sha }}
      - id: build-web
        name: 构建 web 镜像
        uses: docker/build-push-action@3b5e8027fcad23fda98b2e3ac259d8d67585f671
        with:
          file: deploy/Dockerfile.web
          push: true
          platforms: linux/amd64
          tags: ghcr.io/nolyone1/yunling-web:sha-${{ github.event.workflow_run.head_sha }}
      - id: build-ops
        name: 构建 ops 镜像
        uses: docker/build-push-action@3b5e8027fcad23fda98b2e3ac259d8d67585f671
        with:
          file: deploy/Dockerfile.ops
          push: true
          platforms: linux/amd64
          tags: ghcr.io/nolyone1/yunling-ops:sha-${{ github.event.workflow_run.head_sha }}
      - name: 生成并校验候选清单
        run: |
          yunling-release manifest create --candidate-run-id "${{ github.run_id }}" --repository-id "${{ github.repository_id }}" --source-sha "${{ github.event.workflow_run.head_sha }}" --services "ghcr.io/nolyone1/yunling-services@${{ steps.build-services.outputs.digest }}" --web "ghcr.io/nolyone1/yunling-web@${{ steps.build-web.outputs.digest }}" --ops "ghcr.io/nolyone1/yunling-ops@${{ steps.build-ops.outputs.digest }}" --repository-root . --agent-lock deploy/agent/release-lock.json --output release-manifest.json
          yunling-release manifest validate --input release-manifest.json
      - name: 证明 services 镜像
        uses: actions/attest@59d89421af93a897026c735860bf21b6eb4f7b26
        with:
          subject-name: ghcr.io/nolyone1/yunling-services
          subject-digest: ${{ steps.build-services.outputs.digest }}
          push-to-registry: true
      - name: 证明 web 镜像
        uses: actions/attest@59d89421af93a897026c735860bf21b6eb4f7b26
        with:
          subject-name: ghcr.io/nolyone1/yunling-web
          subject-digest: ${{ steps.build-web.outputs.digest }}
          push-to-registry: true
      - name: 证明 ops 镜像
        uses: actions/attest@59d89421af93a897026c735860bf21b6eb4f7b26
        with:
          subject-name: ghcr.io/nolyone1/yunling-ops
          subject-digest: ${{ steps.build-ops.outputs.digest }}
          push-to-registry: true
      - name: 证明 bootstrap 包
        uses: actions/attest@59d89421af93a897026c735860bf21b6eb4f7b26
        with:
          subject-path: yunling-release-bootstrap.tar.gz
      - name: 上传候选产物
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02
        with:
          name: yunling-release-${{ github.event.workflow_run.head_sha }}
          path: |
            release-manifest.json
            SHA256SUMS
            yunling-release-bootstrap.tar.gz
          retention-days: 90
          if-no-files-found: error
`

func TestValidateWorkflowFilesRejectsCandidateSecurityRegressions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"错误触发工作流", func(root map[string]any) {
			workflowRun(root)["workflows"] = []any{"其他 CI"}
		}},
		{"非完成事件", func(root map[string]any) {
			workflowRun(root)["types"] = []any{"requested"}
		}},
		{"扩大顶层权限", func(root map[string]any) {
			root["permissions"].(map[string]any)["actions"] = "write"
		}},
		{"错误保留期", func(root map[string]any) {
			stepByUse(root, "actions/upload-artifact")["with"].(map[string]any)["retention-days"] = 30
		}},
		{"缺少镜像证明", func(root map[string]any) {
			steps := workflowSteps(root)
			for index, step := range steps {
				if step.(map[string]any)["name"] == "证明 ops 镜像" {
					publishJob(root)["steps"] = append(steps[:index], steps[index+1:]...)
					return
				}
			}
		}},
		{"可变 Action 引用", func(root map[string]any) {
			stepByUse(root, "actions/checkout")["uses"] = "actions/checkout@v5"
		}},
		{"候选包含 SSH", func(root map[string]any) {
			publishJob(root)["steps"] = append(workflowSteps(root), map[string]any{"run": "ssh production.example"})
		}},
		{"候选引用生产密钥", func(root map[string]any) {
			publishJob(root)["env"] = map[string]any{"PRODUCTION_SSH_KEY": "${{ secrets.PRODUCTION_SSH_KEY }}"}
		}},
		{"发布代理镜像", func(root map[string]any) {
			stepByID(root, "build-services")["with"].(map[string]any)["tags"] = "ghcr.io/nolyone1/yunling-agent:latest"
		}},
		{"错误镜像仓库登录", func(root map[string]any) {
			stepByUse(root, "docker/login-action")["with"].(map[string]any)["registry"] = "docker.io"
		}},
		{"缺少清单复核", func(root map[string]any) {
			for _, value := range workflowSteps(root) {
				step := value.(map[string]any)
				if run, ok := step["run"].(string); ok && len(run) > 0 {
					step["run"] = strings.ReplaceAll(run, "yunling-release manifest validate --input release-manifest.json", "true")
				}
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := decodeWorkflowFixture(t)
			test.mutate(root)
			directory := t.TempDir()
			writeWorkflowMap(t, directory, root)
			if err := ValidateWorkflowFiles(directory); !errors.Is(err, ErrUnsafeWorkflow) {
				t.Fatalf("不安全候选工作流必须失败：%v", err)
			}
		})
	}
}

func TestValidateWorkflowFilesAcceptsCandidatePolicyFixture(t *testing.T) {
	directory := t.TempDir()
	writeWorkflowMap(t, directory, decodeWorkflowFixture(t))
	if err := ValidateWorkflowFiles(directory); err != nil {
		t.Fatalf("合法候选工作流被拒绝：%v", err)
	}
}

func decodeWorkflowFixture(t *testing.T) map[string]any {
	t.Helper()
	var root map[string]any
	if err := yaml.Unmarshal([]byte(validCandidateWorkflowFixture), &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeWorkflowMap(t *testing.T, root string, workflow map[string]any) {
	t.Helper()
	directory := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := yaml.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "publish-candidate.yml"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func workflowRun(root map[string]any) map[string]any {
	return root["on"].(map[string]any)["workflow_run"].(map[string]any)
}

func publishJob(root map[string]any) map[string]any {
	return root["jobs"].(map[string]any)["publish"].(map[string]any)
}

func workflowSteps(root map[string]any) []any {
	return publishJob(root)["steps"].([]any)
}

func stepByUse(root map[string]any, prefix string) map[string]any {
	for _, value := range workflowSteps(root) {
		step := value.(map[string]any)
		if uses, ok := step["uses"].(string); ok && len(uses) >= len(prefix) && uses[:len(prefix)] == prefix {
			return step
		}
	}
	panic("step not found: " + prefix)
}

func stepByID(root map[string]any, id string) map[string]any {
	for _, value := range workflowSteps(root) {
		step := value.(map[string]any)
		if step["id"] == id {
			return step
		}
	}
	panic("step not found: " + id)
}
