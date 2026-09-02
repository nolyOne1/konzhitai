package main

import (
	"bytes"
	"io"
	"testing"

	"yunling.local/platform/internal/executor"
)

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
