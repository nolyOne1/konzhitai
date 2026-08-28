//go:build linux

package executor_test

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestRunnerCreatesGroupWritableDirectoryForDedicatedUser(t *testing.T) {
	previousUmask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(previousUmask) })
	launcher := newFakeLauncher(true)
	runner, assignment := newTestRunner(t, launcher, time.Second)
	events, err := runner.Start(context.Background(), assignment)
	if err != nil {
		t.Fatalf("启动测试任务：%v", err)
	}
	info, err := os.Stat(launcher.spec.WorkingDirectory)
	if err != nil {
		t.Fatalf("读取独立运行目录权限：%v", err)
	}
	if info.Mode().Perm() != 0o770 {
		t.Fatalf("运行目录必须允许专用账号所在组写入：got=%o want=770", info.Mode().Perm())
	}
	if err := runner.Cancel(context.Background(), assignment.RunID, assignment.ExecutionToken); err != nil {
		t.Fatalf("结束测试任务：%v", err)
	}
	_ = collectEventTypes(t, events)
}
