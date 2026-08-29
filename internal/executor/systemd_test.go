package executor

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

func TestRunSystemdSpecExecutesArgumentArrayWithoutShell(t *testing.T) {
	if os.Getenv("YUNLING_SYSTEMD_SPEC_HELPER") == "1" {
		separator := -1
		for index, argument := range os.Args {
			if argument == "--" {
				separator = index
				break
			}
		}
		if separator < 0 {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("YUNLING_SYSTEMD_SPEC_RESULT"), []byte(strings.Join(os.Args[separator+1:], "\n")+"\n"+os.Getenv("YUNLING_PARAMETER")), 0o600)
		os.Exit(0)
	}

	directory := t.TempDir()
	resultPath := filepath.Join(directory, "result.txt")
	specPath := filepath.Join(directory, systemdSpecFileName)
	body, err := json.Marshal(systemdRunSpec{
		Arguments:        []string{os.Args[0], "-test.run=TestRunSystemdSpecExecutesArgumentArrayWithoutShell", "--", "a; rm -rf /", "中文参数"},
		Environment:      map[string]string{"YUNLING_SYSTEMD_SPEC_HELPER": "1", "YUNLING_SYSTEMD_SPEC_RESULT": resultPath, "YUNLING_PARAMETER": "值; echo 不执行"},
		WorkingDirectory: directory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if exitCode, err := RunSystemdSpec(specPath); err != nil || exitCode != 0 {
		t.Fatalf("执行规格失败：exit=%d err=%v", exitCode, err)
	}
	if _, err := os.Stat(specPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("启动进程后必须立即清除含敏感环境变量的执行规格：%v", err)
	}
	result, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != "a; rm -rf /\n中文参数\n值; echo 不执行" {
		t.Fatalf("参数或环境值被 shell 解释：%q", result)
	}
}

func TestBuildSystemdCommandUsesFixedRunnerTemplateWithoutShell(t *testing.T) {
	workDir := t.TempDir()
	script := exec.Command("python3", "/cache/script.py", "--name", "a; rm -rf /")
	script.Path = "python3"
	command, err := buildSystemdCommand(LaunchSpec{
		RunID:            "run-1",
		Command:          script,
		WorkingDirectory: workDir,
		Environment:      map[string]string{"YUNLING_PARAMETER": "值; echo 非法"},
		Resources: agentprotocol.ResourceLimits{
			CPUMillicores: 250,
			MemoryBytes:   128 << 20,
			TasksMax:      32,
		},
		Timeout: 61 * time.Second,
	})
	if err != nil {
		t.Fatalf("构建 systemd 模板单元命令：%v", err)
	}
	want := []string{
		"systemctl",
		"start",
		"--wait",
		"--no-ask-password",
		"yunling-run@run-1.service",
	}
	if command.Path != "systemctl" || !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("systemd 模板启动参数不正确：\ngot=%q\nwant=%q", command.Args, want)
	}
	body, err := os.ReadFile(filepath.Join(workDir, systemdSpecFileName))
	if err != nil {
		t.Fatalf("读取隔离执行规格：%v", err)
	}
	var stored systemdRunSpec
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatalf("解析隔离执行规格：%v", err)
	}
	if !reflect.DeepEqual(stored.Arguments, script.Args) || stored.Environment["YUNLING_PARAMETER"] != "值; echo 非法" || stored.WorkingDirectory != workDir {
		t.Fatalf("隔离执行规格不得经过 shell 拼接：%+v", stored)
	}
}

func TestBuildSystemdKillCommandTargetsEveryProcessInUnit(t *testing.T) {
	terminate := buildSystemdKillCommand("yunling-run@run-1.service", "TERM")
	wantTerminate := []string{"systemctl", "kill", "--kill-who=all", "--signal=TERM", "yunling-run@run-1.service"}
	if terminate.Path != "systemctl" || !reflect.DeepEqual(terminate.Args, wantTerminate) {
		t.Fatalf("正常终止必须覆盖临时单元全部进程：got=%q want=%q", terminate.Args, wantTerminate)
	}
	kill := buildSystemdKillCommand("yunling-run@run-1.service", "KILL")
	wantKill := []string{"systemctl", "kill", "--kill-who=all", "--signal=KILL", "yunling-run@run-1.service"}
	if kill.Path != "systemctl" || !reflect.DeepEqual(kill.Args, wantKill) {
		t.Fatalf("强制终止必须覆盖临时单元全部进程：got=%q want=%q", kill.Args, wantKill)
	}
}

func TestSystemdProcessStreamsLogsBeforeTaskFinishes(t *testing.T) {
	directory := t.TempDir()
	specPath := filepath.Join(directory, systemdSpecFileName)
	stdoutPath := filepath.Join(directory, systemdStdoutFileName)
	stderrPath := filepath.Join(directory, systemdStderrFileName)
	for _, path := range []string{specPath, stdoutPath, stderrPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	underlying := &blockingSystemdTestProcess{done: make(chan struct{})}
	stdout := &synchronizedBuffer{}
	process := &systemdProcess{Process: underlying, specPath: specPath, stdoutPath: stdoutPath, stderrPath: stderrPath, stdout: stdout}
	finished := make(chan error, 1)
	go func() {
		_, err := process.Wait()
		finished <- err
	}()
	if err := os.WriteFile(stdoutPath, []byte("实时日志"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for stdout.String() != "实时日志" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if stdout.String() != "实时日志" {
		t.Fatal("任务结束前没有转发新增日志")
	}
	close(underlying.done)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(specPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("任务结束后必须清除含敏感环境变量的执行规格：%v", err)
	}
	for _, path := range []string{stdoutPath, stderrPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("日志转发完成后必须删除本地临时副本 %q：%v", path, err)
		}
	}
}

type blockingSystemdTestProcess struct{ done chan struct{} }

func (p *blockingSystemdTestProcess) Wait() (int, error) { <-p.done; return 0, nil }
func (p *blockingSystemdTestProcess) Terminate() error   { return nil }
func (p *blockingSystemdTestProcess) KillGroup() error   { return nil }

type synchronizedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *synchronizedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(value)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}
