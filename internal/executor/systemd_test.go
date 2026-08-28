package executor

import (
	"os/exec"
	"reflect"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

func TestBuildSystemdCommandAppliesResourceLimitsWithoutShell(t *testing.T) {
	script := exec.Command("python3", "/cache/script.py", "--name", "a; rm -rf /")
	script.Path = "python3"
	command, err := buildSystemdCommand(LaunchSpec{
		RunID:            "run-1",
		Command:          script,
		WorkingDirectory: "/var/lib/yunling-agent/runs/run-1",
		Environment:      map[string]string{"YUNLING_PARAMETER": "值; echo 非法"},
		Resources: agentprotocol.ResourceLimits{
			CPUMillicores: 250,
			MemoryBytes:   128 << 20,
			TasksMax:      32,
		},
		Timeout: 61 * time.Second,
	})
	if err != nil {
		t.Fatalf("构建 systemd 临时单元命令：%v", err)
	}
	want := []string{
		"systemd-run",
		"--unit=yunling-run-run-1",
		"--uid=yunling-runner",
		"--wait",
		"--collect",
		"--quiet",
		"--property=CPUQuota=25%",
		"--property=MemoryMax=134217728",
		"--property=TasksMax=32",
		"--property=RuntimeMaxSec=61",
		"--property=NoNewPrivileges=yes",
		"--property=PrivateTmp=yes",
		"--property=PrivateDevices=yes",
		"--property=ProtectSystem=strict",
		"--property=ProtectHome=yes",
		"--property=ProtectKernelTunables=yes",
		"--property=ProtectKernelModules=yes",
		"--property=ProtectControlGroups=yes",
		"--property=RestrictSUIDSGID=yes",
		"--property=LockPersonality=yes",
		"--property=ReadWritePaths=/var/lib/yunling-agent/runs/run-1",
		"--working-directory=/var/lib/yunling-agent/runs/run-1",
		"--setenv=YUNLING_PARAMETER=值; echo 非法",
		"--",
		"python3",
		"/cache/script.py",
		"--name",
		"a; rm -rf /",
	}
	if command.Path != "systemd-run" || !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("systemd-run 参数边界或资源限制不正确：\ngot=%q\nwant=%q", command.Args, want)
	}
}

func TestBuildSystemdKillCommandTargetsEveryProcessInUnit(t *testing.T) {
	terminate := buildSystemdKillCommand("yunling-run-run-1", "TERM")
	wantTerminate := []string{"systemctl", "kill", "--kill-who=all", "--signal=TERM", "yunling-run-run-1"}
	if terminate.Path != "systemctl" || !reflect.DeepEqual(terminate.Args, wantTerminate) {
		t.Fatalf("正常终止必须覆盖临时单元全部进程：got=%q want=%q", terminate.Args, wantTerminate)
	}
	kill := buildSystemdKillCommand("yunling-run-run-1", "KILL")
	wantKill := []string{"systemctl", "kill", "--kill-who=all", "--signal=KILL", "yunling-run-run-1"}
	if kill.Path != "systemctl" || !reflect.DeepEqual(kill.Args, wantKill) {
		t.Fatalf("强制终止必须覆盖临时单元全部进程：got=%q want=%q", kill.Args, wantKill)
	}
}
