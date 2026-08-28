package executor_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/executor"
)

func TestRunnerKillsProcessGroupOnTimeout(t *testing.T) {
	launcher := newFakeLauncher(false)
	runner, assignment := newTestRunner(t, launcher, time.Millisecond)
	assignment.Timeout = time.Millisecond

	events, err := runner.Start(context.Background(), assignment)
	if err != nil {
		t.Fatalf("启动测试任务：%v", err)
	}
	types := collectEventTypes(t, events)
	if !containsEventType(types, executor.EventTimedOut) {
		t.Fatalf("超时后必须上报超时事件：%v", types)
	}
	if !launcher.process.wasTerminated() || !launcher.process.wasGroupKilled() {
		t.Fatalf("超时必须先正常终止再终止整个进程组：terminated=%v killed=%v", launcher.process.wasTerminated(), launcher.process.wasGroupKilled())
	}
}

func TestRunnerCancelRequiresMatchingExecutionToken(t *testing.T) {
	launcher := newFakeLauncher(true)
	runner, assignment := newTestRunner(t, launcher, time.Second)
	events, err := runner.Start(context.Background(), assignment)
	if err != nil {
		t.Fatalf("启动测试任务：%v", err)
	}
	if runner.RunningCount() != 1 {
		t.Fatalf("运行中的任务数应为 1，实际为 %d", runner.RunningCount())
	}
	if err := runner.Cancel(context.Background(), assignment.RunID, "stale-token"); !errors.Is(err, executor.ErrExecutionTokenMismatch) {
		t.Fatalf("旧执行令牌必须被拒绝：%v", err)
	}
	if launcher.process.wasTerminated() {
		t.Fatal("错误执行令牌不得终止进程")
	}
	if err := runner.Cancel(context.Background(), assignment.RunID, assignment.ExecutionToken); err != nil {
		t.Fatalf("使用当前执行令牌取消：%v", err)
	}
	types := collectEventTypes(t, events)
	if !containsEventType(types, executor.EventCancelled) || !launcher.process.wasTerminated() {
		t.Fatalf("取消必须正常终止并上报已取消：types=%v terminated=%v", types, launcher.process.wasTerminated())
	}
	if runner.RunningCount() != 0 {
		t.Fatalf("任务结束后运行中的任务数应归零，实际为 %d", runner.RunningCount())
	}
}

func TestRunnerUsesDedicatedWorkingDirectoryAndAllowedScriptRoot(t *testing.T) {
	launcher := newFakeLauncher(true)
	runner, assignment := newTestRunner(t, launcher, time.Second)
	events, err := runner.Start(context.Background(), assignment)
	if err != nil {
		t.Fatalf("启动测试任务：%v", err)
	}
	if err := runner.Cancel(context.Background(), assignment.RunID, assignment.ExecutionToken); err != nil {
		t.Fatalf("结束测试任务：%v", err)
	}
	_ = collectEventTypes(t, events)

	wantDirectory := filepath.Join(filepath.Dir(filepath.Dir(assignment.ScriptPath)), "runs", assignment.RunID)
	if launcher.spec.WorkingDirectory != wantDirectory {
		t.Fatalf("每次运行必须使用独立目录：got=%q want=%q", launcher.spec.WorkingDirectory, wantDirectory)
	}
	info, err := os.Stat(wantDirectory)
	if err != nil || !info.IsDir() {
		t.Fatalf("独立运行目录未创建：%v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.py")
	if err := os.WriteFile(outside, []byte("print('outside')\n"), 0o640); err != nil {
		t.Fatalf("创建越界脚本：%v", err)
	}
	assignment.RunID = "run-2"
	assignment.ExecutionToken = "token-2"
	assignment.ScriptPath = outside
	if _, err := runner.Start(context.Background(), assignment); !errors.Is(err, executor.ErrScriptPathNotAllowed) {
		t.Fatalf("缓存目录外脚本必须被拒绝：%v", err)
	}
}

func TestRunnerRejectsInvalidEnvironmentVariableNames(t *testing.T) {
	launcher := newFakeLauncher(true)
	runner, assignment := newTestRunner(t, launcher, time.Second)
	assignment.Environment = map[string]string{"BAD=NAME": "value"}
	if _, err := runner.Start(context.Background(), assignment); !errors.Is(err, executor.ErrInvalidAssignment) {
		t.Fatalf("无效环境变量名必须在启动前被拒绝：%v", err)
	}
}

func TestRunnerConnectsStdoutAndStderrToOutputSink(t *testing.T) {
	launcher := newFakeLauncher(true)
	sink := &fakeOutputSink{writers: map[string]*bytes.Buffer{"stdout": {}, "stderr": {}}}
	runner, assignment := newTestRunner(t, launcher, time.Second, executor.WithOutputSink(sink))
	if _, err := runner.Start(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if launcher.spec.Stdout == nil || launcher.spec.Stderr == nil {
		t.Fatal("启动任务时必须连接标准输出和标准错误日志流")
	}
	_, _ = launcher.spec.Stdout.Write([]byte("输出"))
	_, _ = launcher.spec.Stderr.Write([]byte("错误"))
	if sink.writers["stdout"].String() != "输出" || sink.writers["stderr"].String() != "错误" {
		t.Fatalf("日志流未写入缓冲：stdout=%q stderr=%q", sink.writers["stdout"], sink.writers["stderr"])
	}
	_ = runner.Cancel(context.Background(), assignment.RunID, assignment.ExecutionToken)
}

func newTestRunner(t *testing.T, launcher *fakeLauncher, grace time.Duration, extra ...executor.RunnerOption) (*executor.Runner, agentprotocol.Assignment) {
	t.Helper()
	root := t.TempDir()
	cacheRoot := filepath.Join(root, "cache")
	if err := os.MkdirAll(cacheRoot, 0o750); err != nil {
		t.Fatalf("创建测试缓存目录：%v", err)
	}
	scriptPath := filepath.Join(cacheRoot, "script.py")
	if err := os.WriteFile(scriptPath, []byte("print('ok')\n"), 0o640); err != nil {
		t.Fatalf("创建测试脚本：%v", err)
	}
	options := []executor.RunnerOption{
		executor.WithWorkRoot(filepath.Join(root, "runs")),
		executor.WithAllowedScriptRoots(cacheRoot),
		executor.WithAllowedRuntimes("python3"),
	}
	options = append(options, extra...)
	runner := executor.NewRunner(
		launcher,
		grace,
		options...,
	)
	return runner, agentprotocol.Assignment{
		RunID:           "run-1",
		ExecutionToken:  "token-1",
		ScriptVersionID: "version-1",
		Runtime:         "python3",
		ScriptPath:      scriptPath,
		Arguments:       []string{"--name", "测试任务"},
		Environment:     map[string]string{"YUNLING_PARAMETER": "值"},
		Resources: agentprotocol.ResourceLimits{
			CPUMillicores: 250,
			MemoryBytes:   128 << 20,
			DiskBytes:     128 << 20,
			TasksMax:      32,
		},
		Timeout: time.Minute,
	}
}

type fakeOutputSink struct{ writers map[string]*bytes.Buffer }

func (s *fakeOutputSink) OutputWriter(_, _, stream string) io.Writer { return s.writers[stream] }

func collectEventTypes(t *testing.T, events <-chan executor.Event) []executor.EventType {
	t.Helper()
	deadline := time.After(time.Second)
	types := make([]executor.EventType, 0, 2)
	for {
		select {
		case event, open := <-events:
			if !open {
				return types
			}
			types = append(types, event.Type)
		case <-deadline:
			t.Fatalf("1 秒内未收到终态事件，已有事件：%v", types)
			return nil
		}
	}
}

func containsEventType(types []executor.EventType, want executor.EventType) bool {
	for _, eventType := range types {
		if eventType == want {
			return true
		}
	}
	return false
}

type fakeLauncher struct {
	process *fakeProcess
	spec    executor.LaunchSpec
}

func newFakeLauncher(exitOnTerminate bool) *fakeLauncher {
	return &fakeLauncher{process: &fakeProcess{done: make(chan struct{}), exitOnTerminate: exitOnTerminate}}
}

func (l *fakeLauncher) Start(_ context.Context, spec executor.LaunchSpec) (executor.Process, error) {
	l.spec = spec
	return l.process, nil
}

type fakeProcess struct {
	mu              sync.Mutex
	done            chan struct{}
	exitOnTerminate bool
	terminated      bool
	groupKilled     bool
	finished        bool
}

func (p *fakeProcess) Wait() (int, error) {
	<-p.done
	return -1, errors.New("进程被测试终止")
}

func (p *fakeProcess) Terminate() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.terminated = true
	if p.exitOnTerminate {
		p.finishLocked()
	}
	return nil
}

func (p *fakeProcess) KillGroup() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.groupKilled = true
	p.finishLocked()
	return nil
}

func (p *fakeProcess) wasTerminated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminated
}

func (p *fakeProcess) wasGroupKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.groupKilled
}

func (p *fakeProcess) finishLocked() {
	if !p.finished {
		close(p.done)
		p.finished = true
	}
}
