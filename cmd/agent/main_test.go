package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/agentupdate"
	"yunling.local/platform/internal/executor"
)

type reconnectEventSender struct{ events []agentprotocol.UpgradeEvent }

func (s *reconnectEventSender) SendUpgradeEvent(_ context.Context, event agentprotocol.UpgradeEvent) error {
	s.events = append(s.events, event)
	return nil
}

func TestConfirmPendingUpgradeMarksMatchingReconnect(t *testing.T) {
	root := t.TempDir()
	commandDir := filepath.Join(root, "upgrade-1")
	if err := os.MkdirAll(commandDir, 0o700); err != nil {
		t.Fatal(err)
	}
	spec, _ := json.Marshal(agentupdate.Spec{CommandID: "upgrade-1", TargetVersion: "0.2.0", Phase: "restarted"})
	if err := os.WriteFile(filepath.Join(commandDir, "spec.json"), spec, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agentupdate.SaveRuntimeState(root, agentprotocol.UpgradeRuntimeState{
		CommandID: "upgrade-1", TargetID: "target-1", TargetVersion: "0.2.0", Stage: agentprotocol.StageInstalling,
	}); err != nil {
		t.Fatal(err)
	}
	sender := &reconnectEventSender{}
	fixedNow := func() time.Time { return time.Date(2026, 9, 7, 4, 0, 0, 0, time.UTC) }
	if err := confirmPendingUpgrade(context.Background(), root, "0.2.0", sender, fixedNow); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 1 || sender.events[0].Stage != agentprotocol.StageReconnecting {
		t.Fatalf("未上报重连阶段：%+v", sender.events)
	}
	if _, err := os.Stat(filepath.Join(commandDir, "connected")); err != nil {
		t.Fatalf("未写入重连标记：%v", err)
	}
}

func TestConfirmPendingUpgradeSkipsWrongVersion(t *testing.T) {
	root := t.TempDir()
	if err := agentupdate.SaveRuntimeState(root, agentprotocol.UpgradeRuntimeState{
		CommandID: "upgrade-1", TargetVersion: "0.2.0", Stage: agentprotocol.StageInstalling,
	}); err != nil {
		t.Fatal(err)
	}
	sender := &reconnectEventSender{}
	if err := confirmPendingUpgrade(context.Background(), root, "0.1.0", sender, time.Now); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 0 {
		t.Fatalf("版本不符时不得确认：%+v", sender.events)
	}
}

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
