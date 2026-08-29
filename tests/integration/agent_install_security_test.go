package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yunling.local/platform/internal/testpostgres"
)

func TestAgentInstallerUsesDedicatedControlAndRunnerAccounts(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	install := mustReadDeploymentFile(t, root, "deploy", "agent", "install.sh")
	service := mustReadDeploymentFile(t, root, "deploy", "agent", "yunling-agent.service")
	runService := mustReadDeploymentFile(t, root, "deploy", "agent", "yunling-run@.service")
	policy := mustReadDeploymentFile(t, root, "deploy", "agent", "50-yunling-agent.rules")

	for _, required := range []string{"useradd --system", "yunling-agent", "yunling-runner"} {
		if !strings.Contains(install, required) {
			t.Fatalf("代理安装器缺少专用账号配置 %q", required)
		}
	}
	if !strings.Contains(install, "systemd_version < 240") {
		t.Fatal("安装器必须拒绝不支持 append 日志输出的旧版 systemd")
	}
	if !strings.Contains(install, `credentials_path="/var/lib/yunling-agent/credentials.json"`) ||
		!strings.Contains(install, `install -d -o root -g yunling-agent -m 0750 /etc/yunling-agent`) {
		t.Fatal("长期凭据必须位于代理可写数据目录，配置目录必须保持 root 所有")
	}
	if !strings.Contains(install, `-m 2750 /var/lib/yunling-agent/script-cache`) ||
		!strings.Contains(install, `-m 2750 /var/lib/yunling-agent/runs`) {
		t.Fatal("业务运行账户不得写入共享脚本缓存或创建任意运行目录")
	}
	if !strings.Contains(service, "User=yunling-agent") || strings.Contains(service, "User=root") ||
		!strings.Contains(service, "NoNewPrivileges=true") {
		t.Fatal("代理控制服务必须以禁止提权的 yunling-agent 专用账号运行")
	}
	if !strings.Contains(runService, "User=yunling-runner") ||
		!strings.Contains(runService, "ExecStart=/usr/local/bin/yunling-agent run-spec") {
		t.Fatal("业务脚本必须由固定的 root 管理模板以 yunling-runner 账号启动")
	}
	if !strings.Contains(policy, `subject.user != "yunling-agent"`) ||
		!strings.Contains(policy, `^yunling-run@[A-Za-z0-9_-]+\.service$`) ||
		strings.Contains(policy, `unit.indexOf("yunling-run-") == 0`) {
		t.Fatal("polkit 规则必须只允许禁止提权的专用代理账号管理固定 yunling-run@ 实例")
	}
	if strings.Contains(policy, "StartTransientUnit") || strings.Contains(policy, "systemd-run") {
		t.Fatal("代理不得获得创建任意 systemd 临时单元的授权")
	}
	if !strings.Contains(policy, "polkit.Result.NO") {
		t.Fatal("代理账户的越权 systemd 请求必须被 polkit 显式拒绝")
	}
}

func mustReadDeploymentFile(t *testing.T, root string, elements ...string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(append([]string{root}, elements...)...))
	if err != nil {
		t.Fatalf("读取部署文件：%v", err)
	}
	return string(body)
}
