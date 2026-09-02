package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCommandRunnerAllowsOnlyPinnedAbsoluteTools(t *testing.T) {
	want := []string{
		"/usr/bin/pg_dump",
		"/usr/bin/pg_restore",
		"/usr/bin/psql",
		"/usr/bin/mc",
		"/usr/bin/restic",
	}
	if strings.Join(AllowedCommands(), "\n") != strings.Join(want, "\n") {
		t.Fatalf("命令白名单错误：%v", AllowedCommands())
	}
	runner := NewCommandRunner(time.Second)
	for _, name := range []string{"pg_dump", "../usr/bin/restic", "/bin/sh", "/usr/bin/env"} {
		if _, err := runner.Run(context.Background(), name, nil, nil); !errors.Is(err, ErrCommandNotAllowed) {
			t.Fatalf("必须拒绝命令 %q，实际错误：%v", name, err)
		}
	}
}

func TestCommandRunnerBoundsOutputAndDoesNotLeakEnvironment(t *testing.T) {
	runner := NewCommandRunner(time.Second)
	runner.commands["/usr/bin/pg_dump"] = os.Args[0]
	secret := "command-secret-must-not-leak"
	result, err := runner.Run(context.Background(), "/usr/bin/pg_dump", []string{
		"-test.run=TestBackupCommandHelperProcess", "--", "large-output",
	}, map[string]string{
		"GO_WANT_BACKUP_COMMAND_HELPER": "1",
		"BACKUP_TEST_SECRET":            secret,
	})
	if err == nil || result.ExitCode != 3 {
		t.Fatalf("辅助进程应以 3 失败：result=%+v err=%v", result, err)
	}
	if len(result.Stdout) != 4096 || len(result.Stderr) != 4096 {
		t.Fatalf("输出必须各限制为 4096 字节：stdout=%d stderr=%d", len(result.Stdout), len(result.Stderr))
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(result.Stdout, secret) || strings.Contains(result.Stderr, secret) {
		t.Fatal("命令结果不得泄露环境变量秘密")
	}
}

func TestCommandRunnerTimesOutAndKillsProcess(t *testing.T) {
	runner := NewCommandRunner(80 * time.Millisecond)
	runner.commands["/usr/bin/psql"] = os.Args[0]
	started := time.Now()
	_, err := runner.Run(context.Background(), "/usr/bin/psql", []string{
		"-test.run=TestBackupCommandHelperProcess", "--", "sleep",
	}, map[string]string{"GO_WANT_BACKUP_COMMAND_HELPER": "1"})
	if !errors.Is(err, ErrCommandTimedOut) {
		t.Fatalf("超时错误不正确：%v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("超时后进程未及时退出：%v", elapsed)
	}
}

func TestCommandRunnerDoesNotInheritUnrelatedParentSecrets(t *testing.T) {
	t.Setenv("YUNLING_DATABASE_URL", "postgres://admin:parent-secret@postgres/yunling")
	runner := NewCommandRunner(time.Second)
	runner.commands["/usr/bin/pg_dump"] = os.Args[0]
	result, err := runner.Run(context.Background(), "/usr/bin/pg_dump", []string{
		"-test.run=TestBackupCommandHelperProcess", "--", "parent-environment",
	}, map[string]string{"GO_WANT_BACKUP_COMMAND_HELPER": "1"})
	if err != nil {
		t.Fatalf("子进程不应继承父进程业务秘密：result=%+v err=%v", result, err)
	}
}

func TestBackupCommandHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_BACKUP_COMMAND_HELPER") != "1" {
		return
	}
	mode := ""
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
			break
		}
	}
	switch mode {
	case "large-output":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("o", 5000))
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("e", 5000))
		os.Exit(3)
	case "sleep":
		time.Sleep(10 * time.Second)
	case "parent-environment":
		if os.Getenv("YUNLING_DATABASE_URL") != "" {
			os.Exit(4)
		}
	}
	os.Exit(0)
}
