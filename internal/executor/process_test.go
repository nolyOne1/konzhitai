package executor_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yunling.local/platform/internal/executor"
)

func TestProcessLauncherUsesWorkingDirectoryAndEnvironment(t *testing.T) {
	workingDirectory := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=TestExecutorHelperProcess", "--")
	var output bytes.Buffer
	command.Stdout = &output
	process, err := executor.NewProcessLauncher().Start(context.Background(), executor.LaunchSpec{
		Command:          command,
		WorkingDirectory: workingDirectory,
		Environment: map[string]string{
			"GO_WANT_YUNLING_HELPER_PROCESS": "1",
			"YUNLING_TEST_VALUE":             "中文参数",
		},
	})
	if err != nil {
		t.Fatalf("启动直接进程：%v", err)
	}
	exitCode, err := process.Wait()
	if err != nil || exitCode != 0 {
		t.Fatalf("等待直接进程：exit=%d err=%v", exitCode, err)
	}
	want := filepath.Clean(workingDirectory) + "|中文参数"
	if strings.TrimSpace(output.String()) != want {
		t.Fatalf("直接进程未收到独立目录或环境变量：got=%q want=%q", output.String(), want)
	}
}

func TestExecutorHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_YUNLING_HELPER_PROCESS") != "1" {
		return
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Printf("%s|%s", filepath.Clean(workingDirectory), os.Getenv("YUNLING_TEST_VALUE"))
	os.Exit(0)
}
