package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"yunling.local/platform/internal/agentupdate"
	"yunling.local/platform/internal/executor"
)

type commandTestSystem struct{}

func (commandTestSystem) DaemonReload(context.Context) error                { return nil }
func (commandTestSystem) RestartAgent(context.Context) error                { return nil }
func (commandTestSystem) WaitForConfirmation(context.Context, string) error { return nil }

func TestRunApplyUpgradeCommandRoutesExactArguments(t *testing.T) {
	called := false
	handled, err := runApplyUpgradeCommand(
		[]string{"yunling-agent", "apply-upgrade", "upgrade_2026-09-07"},
		"/upgrade-root",
		commandTestSystem{},
		func(root, commandID string, system agentupdate.SystemController) error {
			called = true
			if root != "/upgrade-root" || commandID != "upgrade_2026-09-07" {
				t.Fatalf("升级参数不正确：root=%q commandID=%q", root, commandID)
			}
			return nil
		},
	)
	if err != nil || !handled || !called {
		t.Fatalf("升级命令必须被执行：handled=%v called=%v err=%v", handled, called, err)
	}
}

func TestRunApplyUpgradeCommandRejectsMissingID(t *testing.T) {
	handled, err := runApplyUpgradeCommand(
		[]string{"yunling-agent", "apply-upgrade"},
		agentupdate.DefaultRoot,
		commandTestSystem{},
		func(string, string, agentupdate.SystemController) error {
			t.Fatal("参数不完整时不得调用升级器")
			return nil
		},
	)
	if !handled || err == nil {
		t.Fatalf("参数不完整的升级命令必须返回用法错误：handled=%v err=%v", handled, err)
	}
}

func TestDetectedCapabilitiesRequiresLinuxUpgradeUnit(t *testing.T) {
	unitPath := filepath.Join(t.TempDir(), "yunling-agent-upgrade@.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\n"), 0o600); err != nil {
		t.Fatalf("创建升级单元测试文件：%v", err)
	}
	stat := func(string) (os.FileInfo, error) { return os.Stat(unitPath) }

	capabilities := detectedCapabilities("linux", stat)
	if len(capabilities) != 1 || capabilities[0] != "self_upgrade_v1" {
		t.Fatalf("完整安装的 Linux 代理必须声明自升级能力：%v", capabilities)
	}
	if capabilities := detectedCapabilities("windows", stat); len(capabilities) != 0 {
		t.Fatalf("非 Linux 代理不得声明自升级能力：%v", capabilities)
	}
	if capabilities := detectedCapabilities("linux", func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }); len(capabilities) != 0 {
		t.Fatalf("缺少升级单元时不得声明自升级能力：%v", capabilities)
	}
}

func TestWriteVersionCommandPrintsBuildVersion(t *testing.T) {
	original := agentVersion
	agentVersion = "9.8.7-test"
	t.Cleanup(func() { agentVersion = original })

	var output bytes.Buffer
	if !writeVersionCommand([]string{"yunling-agent", "version"}, &output) {
		t.Fatal("version 子命令必须被处理")
	}
	if output.String() != "9.8.7-test\n" {
		t.Fatalf("版本输出：%q", output.String())
	}
}

func TestWriteVersionCommandIgnoresNormalStart(t *testing.T) {
	if writeVersionCommand([]string{"yunling-agent"}, io.Discard) {
		t.Fatal("普通启动不得被截断")
	}
}

func TestNewAgentLauncherUsesSystemdOnLinux(t *testing.T) {
	launcher := newAgentLauncher("linux", "")
	if _, ok := launcher.(*executor.SystemdLauncher); !ok {
		t.Fatalf("Linux 默认必须使用 systemd 临时单元，实际为 %T", launcher)
	}
}

func TestNewAgentLauncherUsesProcessFallbackOutsideLinux(t *testing.T) {
	launcher := newAgentLauncher("windows", "")
	if _, ok := launcher.(*executor.ProcessLauncher); !ok {
		t.Fatalf("非 Linux 系统必须使用进程组后备执行器，实际为 %T", launcher)
	}
}
