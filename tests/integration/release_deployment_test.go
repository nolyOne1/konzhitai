package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"yunling.local/platform/internal/release"
	"yunling.local/platform/internal/testpostgres"
)

func TestAPIDeploymentBuildSourcesArePinnedAndSensitiveContextIsExcluded(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	if err := release.ValidateBuildSources(root); err != nil {
		t.Fatalf("真实生产构建来源不符合策略：%v", err)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("当前环境没有 Docker，真实 BuildKit 上下文探针留给 Linux CI")
	}
	if output, err := exec.Command("docker", "buildx", "version").CombinedOutput(); err != nil {
		t.Skipf("当前环境没有 Docker Buildx：%v (%s)", err, output)
	}

	contextRoot := t.TempDir()
	for _, relative := range []string{".dockerignore", "deploy/release/testdata/context-probe.Dockerfile"} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		targetName := filepath.Base(relative)
		if err := os.WriteFile(filepath.Join(contextRoot, targetName), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(contextRoot, "included.txt"), []byte("included"), 0o600); err != nil {
		t.Fatal(err)
	}
	sentinels := []string{
		"nested/.env", "deploy/secrets/sentinel", "deploy/agent/sentinel",
		"releases/sentinel", "nested/backups/sentinel", "nested/private.pem",
		"id_rsa_production", "keys/id_ed25519_production", "bin/sentinel", "outputs/sentinel",
	}
	for _, relative := range sentinels {
		path := filepath.Join(contextRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("must-not-enter-context"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	exported := filepath.Join(t.TempDir(), "buildkit-output")
	command := exec.Command("docker", "buildx", "build", "--progress=plain",
		"--file", filepath.Join(contextRoot, "context-probe.Dockerfile"),
		"--output", "type=local,dest="+exported, contextRoot)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("BuildKit 上下文探针失败：%v\n%s", err, output)
	}
	exportedContext := filepath.Join(exported, "context")
	if body, err := os.ReadFile(filepath.Join(exportedContext, "included.txt")); err != nil || string(body) != "included" {
		t.Fatalf("上下文探针未导出安全文件：body=%q err=%v", body, err)
	}
	for _, relative := range sentinels {
		if _, err := os.Lstat(filepath.Join(exportedContext, filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Fatalf("敏感哨兵进入构建上下文：%s (err=%v)", relative, err)
		}
	}
}
