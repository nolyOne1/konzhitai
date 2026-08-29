package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

type EventType string

const (
	EventStarted   EventType = "started"
	EventSucceeded EventType = "succeeded"
	EventFailed    EventType = "failed"
	EventTimedOut  EventType = "timed_out"
	EventCancelled EventType = "cancelled"
)

var (
	ErrInvalidAssignment      = errors.New("任务分配信息无效")
	ErrScriptPathNotAllowed   = errors.New("脚本路径不在允许的缓存目录内")
	ErrRunAlreadyActive       = errors.New("运行实例已在执行")
	ErrRunNotRunning          = errors.New("运行实例当前未执行")
	ErrExecutionTokenMismatch = errors.New("执行令牌与当前运行不匹配")
)

type Event struct {
	Sequence   uint64    `json:"sequence"`
	Type       EventType `json:"type"`
	OccurredAt time.Time `json:"occurred_at"`
	ExitCode   int       `json:"exit_code,omitempty"`
	Message    string    `json:"message,omitempty"`
}

type LaunchSpec struct {
	RunID            string
	ExecutionToken   string
	Command          *exec.Cmd
	WorkingDirectory string
	Environment      map[string]string
	Resources        agentprotocol.ResourceLimits
	Timeout          time.Duration
	Stdout           io.Writer
	Stderr           io.Writer
}

type OutputSink interface {
	OutputWriter(runID, executionToken, stream string) io.Writer
}

type Process interface {
	Wait() (exitCode int, err error)
	Terminate() error
	KillGroup() error
}

type Launcher interface {
	Start(context.Context, LaunchSpec) (Process, error)
}

type RunnerOption func(*Runner)

func WithWorkRoot(root string) RunnerOption {
	return func(runner *Runner) {
		if strings.TrimSpace(root) != "" {
			runner.workRoot = root
		}
	}
}

func WithAllowedScriptRoots(roots ...string) RunnerOption {
	return func(runner *Runner) {
		runner.allowedScriptRoots = append([]string(nil), roots...)
	}
}

func WithAllowedRuntimes(runtimes ...string) RunnerOption {
	return func(runner *Runner) {
		runner.allowedRuntimes = make(map[string]bool, len(runtimes))
		for _, runtimeName := range runtimes {
			runtimeName = strings.TrimSpace(runtimeName)
			if runtimeName != "" {
				runner.allowedRuntimes[runtimeName] = true
			}
		}
	}
}

func WithOutputSink(sink OutputSink) RunnerOption {
	return func(runner *Runner) { runner.output = sink }
}

type Runner struct {
	launcher           Launcher
	stopGrace          time.Duration
	workRoot           string
	allowedScriptRoots []string
	allowedRuntimes    map[string]bool
	now                func() time.Time
	output             OutputSink

	mu     sync.Mutex
	active map[string]*activeRun
}

type activeRun struct {
	token string
	stop  chan stopRequest
	done  chan struct{}
}

type stopRequest struct {
	typeName EventType
	result   chan error
}

type processResult struct {
	exitCode int
	err      error
}

func NewRunner(launcher Launcher, stopGrace time.Duration, options ...RunnerOption) *Runner {
	if stopGrace <= 0 {
		stopGrace = 10 * time.Second
	}
	runner := &Runner{
		launcher:           launcher,
		stopGrace:          stopGrace,
		workRoot:           "/var/lib/yunling-agent/runs",
		allowedScriptRoots: []string{"/var/lib/yunling-agent/script-cache/scripts"},
		allowedRuntimes:    map[string]bool{"bash": true, "python3": true},
		now:                time.Now,
		active:             map[string]*activeRun{},
	}
	for _, option := range options {
		option(runner)
	}
	return runner
}

func (r *Runner) Start(ctx context.Context, assignment agentprotocol.Assignment) (<-chan Event, error) {
	if err := r.validateAssignment(assignment); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if active, exists := r.active[assignment.RunID]; exists {
		r.mu.Unlock()
		if active.token == assignment.ExecutionToken {
			return nil, ErrRunAlreadyActive
		}
		return nil, ErrExecutionTokenMismatch
	}
	r.mu.Unlock()
	scriptPath, err := r.resolveScriptPath(assignment.ScriptPath)
	if err != nil {
		return nil, err
	}
	workingDirectory, err := r.prepareWorkingDirectory(assignment.RunID)
	if err != nil {
		return nil, err
	}
	command, err := BuildCommand(assignment.Runtime, scriptPath, assignment.Arguments)
	if err != nil {
		return nil, err
	}
	spec := LaunchSpec{
		RunID:            assignment.RunID,
		ExecutionToken:   assignment.ExecutionToken,
		Command:          command,
		WorkingDirectory: workingDirectory,
		Environment:      cloneEnvironment(assignment.Environment),
		Resources:        assignment.Resources,
		Timeout:          assignment.Timeout,
	}
	if r.output != nil {
		spec.Stdout = r.output.OutputWriter(assignment.RunID, assignment.ExecutionToken, "stdout")
		spec.Stderr = r.output.OutputWriter(assignment.RunID, assignment.ExecutionToken, "stderr")
	}

	r.mu.Lock()
	if active, exists := r.active[assignment.RunID]; exists {
		r.mu.Unlock()
		if active.token == assignment.ExecutionToken {
			return nil, ErrRunAlreadyActive
		}
		return nil, ErrExecutionTokenMismatch
	}
	process, err := r.launcher.Start(ctx, spec)
	if err != nil {
		r.mu.Unlock()
		return nil, fmt.Errorf("启动隔离任务：%w", err)
	}
	active := &activeRun{
		token: assignment.ExecutionToken,
		stop:  make(chan stopRequest),
		done:  make(chan struct{}),
	}
	r.active[assignment.RunID] = active
	r.mu.Unlock()

	events := make(chan Event, 2)
	events <- Event{Sequence: 1, Type: EventStarted, OccurredAt: r.now().UTC(), Message: "任务已开始执行"}
	go r.supervise(ctx, assignment, process, active, events)
	return events, nil
}

func (r *Runner) Cancel(ctx context.Context, runID, executionToken string) error {
	r.mu.Lock()
	active, exists := r.active[runID]
	if !exists {
		r.mu.Unlock()
		return ErrRunNotRunning
	}
	if active.token != executionToken {
		r.mu.Unlock()
		return ErrExecutionTokenMismatch
	}
	r.mu.Unlock()

	request := stopRequest{typeName: EventCancelled, result: make(chan error, 1)}
	select {
	case active.stop <- request:
	case <-active.done:
		return ErrRunNotRunning
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) RunningCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

func (r *Runner) RunningProcesses() []agentprotocol.RunningProcess {
	r.mu.Lock()
	defer r.mu.Unlock()
	processes := make([]agentprotocol.RunningProcess, 0, len(r.active))
	for runID, active := range r.active {
		processes = append(processes, agentprotocol.RunningProcess{RunID: runID, ExecutionToken: active.token})
	}
	return processes
}

func (r *Runner) supervise(ctx context.Context, assignment agentprotocol.Assignment, process Process, active *activeRun, events chan<- Event) {
	defer func() {
		r.mu.Lock()
		if r.active[assignment.RunID] == active {
			delete(r.active, assignment.RunID)
		}
		r.mu.Unlock()
		close(active.done)
		close(events)
	}()

	finished := make(chan processResult, 1)
	go func() {
		exitCode, err := process.Wait()
		finished <- processResult{exitCode: exitCode, err: err}
	}()
	timer := time.NewTimer(assignment.Timeout)
	defer timer.Stop()

	select {
	case result := <-finished:
		events <- r.exitEvent(2, result)
	case <-timer.C:
		result, stopErr := r.stopProcess(ctx, process, finished)
		message := "任务执行超时，已终止整个进程组"
		if stopErr != nil {
			message += "：" + stopErr.Error()
		}
		events <- Event{Sequence: 2, Type: EventTimedOut, OccurredAt: r.now().UTC(), ExitCode: result.exitCode, Message: message}
	case request := <-active.stop:
		result, stopErr := r.stopProcess(ctx, process, finished)
		events <- Event{Sequence: 2, Type: request.typeName, OccurredAt: r.now().UTC(), ExitCode: result.exitCode, Message: "任务已取消"}
		request.result <- stopErr
	case <-ctx.Done():
		result, _ := r.stopProcess(context.Background(), process, finished)
		events <- Event{Sequence: 2, Type: EventCancelled, OccurredAt: r.now().UTC(), ExitCode: result.exitCode, Message: "代理停止，任务已终止"}
	}
}

func (r *Runner) stopProcess(ctx context.Context, process Process, finished <-chan processResult) (processResult, error) {
	terminateErr := process.Terminate()
	timer := time.NewTimer(r.stopGrace)
	defer timer.Stop()
	select {
	case result := <-finished:
		return result, terminateErr
	case <-timer.C:
		killErr := process.KillGroup()
		select {
		case result := <-finished:
			return result, errors.Join(terminateErr, killErr)
		case <-ctx.Done():
			return processResult{exitCode: -1, err: ctx.Err()}, errors.Join(terminateErr, killErr, ctx.Err())
		}
	case <-ctx.Done():
		killErr := process.KillGroup()
		return processResult{exitCode: -1, err: ctx.Err()}, errors.Join(terminateErr, killErr, ctx.Err())
	}
}

func (r *Runner) exitEvent(sequence uint64, result processResult) Event {
	if result.err == nil && result.exitCode == 0 {
		return Event{Sequence: sequence, Type: EventSucceeded, OccurredAt: r.now().UTC(), Message: "任务执行成功"}
	}
	message := "任务执行失败"
	if result.err != nil {
		message += "：" + result.err.Error()
	}
	return Event{Sequence: sequence, Type: EventFailed, OccurredAt: r.now().UTC(), ExitCode: result.exitCode, Message: message}
}

func (r *Runner) validateAssignment(assignment agentprotocol.Assignment) error {
	if r.launcher == nil || !validPathID(assignment.RunID) || strings.TrimSpace(assignment.ExecutionToken) == "" ||
		strings.TrimSpace(assignment.ScriptVersionID) == "" || !r.allowedRuntimes[assignment.Runtime] ||
		assignment.Timeout <= 0 || assignment.Resources.CPUMillicores <= 0 ||
		assignment.Resources.MemoryBytes <= 0 || assignment.Resources.DiskBytes <= 0 || assignment.Resources.TasksMax <= 0 {
		return ErrInvalidAssignment
	}
	for key := range assignment.Environment {
		if !validEnvironmentName(key) {
			return ErrInvalidAssignment
		}
	}
	return nil
}

func validEnvironmentName(name string) bool {
	if name == "" || !((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z') || name[0] == '_') {
		return false
	}
	for index := 1; index < len(name); index++ {
		character := name[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return true
}

func (r *Runner) resolveScriptPath(configuredPath string) (string, error) {
	absolutePath, err := filepath.Abs(strings.TrimSpace(configuredPath))
	if err != nil {
		return "", fmt.Errorf("%w：解析绝对路径：%v", ErrScriptPathNotAllowed, err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return "", fmt.Errorf("%w：解析真实路径 %q：%v", ErrScriptPathNotAllowed, absolutePath, err)
	}
	info, err := os.Stat(resolvedPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w：脚本不是普通文件 %q：%v", ErrScriptPathNotAllowed, resolvedPath, err)
	}
	for _, configuredRoot := range r.allowedScriptRoots {
		absoluteRoot, rootErr := filepath.Abs(configuredRoot)
		if rootErr != nil {
			continue
		}
		resolvedRoot, rootErr := filepath.EvalSymlinks(absoluteRoot)
		if rootErr == nil && pathInside(resolvedRoot, resolvedPath) {
			return resolvedPath, nil
		}
	}
	return "", fmt.Errorf("%w：脚本=%q，允许目录=%q", ErrScriptPathNotAllowed, resolvedPath, r.allowedScriptRoots)
}

func (r *Runner) prepareWorkingDirectory(runID string) (string, error) {
	root, err := filepath.Abs(r.workRoot)
	if err != nil {
		return "", fmt.Errorf("解析运行目录：%w", err)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return "", fmt.Errorf("创建运行根目录：%w", err)
	}
	workingDirectory := filepath.Join(root, runID)
	if !pathInside(root, workingDirectory) {
		return "", ErrInvalidAssignment
	}
	if err := os.MkdirAll(workingDirectory, 0o770); err != nil {
		return "", fmt.Errorf("创建独立运行目录：%w", err)
	}
	info, err := os.Lstat(workingDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("独立运行目录无效")
	}
	return workingDirectory, nil
}

func cloneEnvironment(environment map[string]string) map[string]string {
	cloned := make(map[string]string, len(environment))
	for key, value := range environment {
		cloned[key] = value
	}
	return cloned
}
