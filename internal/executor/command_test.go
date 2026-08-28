package executor_test

import (
	"testing"

	"yunling.local/platform/internal/executor"
)

func TestBuildCommandKeepsArgumentBoundaries(t *testing.T) {
	command, err := executor.BuildCommand("python3", "/cache/script.py", []string{"--name", "a; rm -rf /"})
	if err != nil {
		t.Fatalf("构建 Python 命令：%v", err)
	}
	want := []string{"python3", "/cache/script.py", "--name", "a; rm -rf /"}
	if command.Path != "python3" {
		t.Fatalf("可执行文件不正确：got=%q want=%q", command.Path, "python3")
	}
	if len(command.Args) != len(want) {
		t.Fatalf("参数数量不正确：got=%q want=%q", command.Args, want)
	}
	for index := range want {
		if command.Args[index] != want[index] {
			t.Fatalf("第 %d 个参数边界被改变：got=%q want=%q", index, command.Args[index], want[index])
		}
	}
}

func TestBuildCommandRejectsUnknownRuntime(t *testing.T) {
	if _, err := executor.BuildCommand("sh -c", "/cache/script.sh", nil); err == nil {
		t.Fatal("未登记的运行环境必须被拒绝")
	}
}

func TestBuildCommandDisablesPowerShellProfilesAndInteractiveMode(t *testing.T) {
	command, err := executor.BuildCommand("powershell", "C:/cache/script.ps1", []string{"-Name", "测试"})
	if err != nil {
		t.Fatalf("构建 PowerShell 命令：%v", err)
	}
	want := []string{"powershell", "-NoProfile", "-NonInteractive", "-File", "C:/cache/script.ps1", "-Name", "测试"}
	if len(command.Args) != len(want) {
		t.Fatalf("PowerShell 安全参数数量不正确：got=%q want=%q", command.Args, want)
	}
	for index := range want {
		if command.Args[index] != want[index] {
			t.Fatalf("PowerShell 第 %d 个参数不正确：got=%q want=%q", index, command.Args[index], want[index])
		}
	}
}
