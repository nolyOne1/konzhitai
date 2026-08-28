package main

import (
	"testing"

	"yunling.local/platform/internal/executor"
)

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
