package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
)

type ProcessLauncher struct{}

func NewProcessLauncher() *ProcessLauncher {
	return &ProcessLauncher{}
}

func (l *ProcessLauncher) Start(ctx context.Context, spec LaunchSpec) (Process, error) {
	if spec.Command == nil || spec.WorkingDirectory == "" {
		return nil, ErrInvalidAssignment
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := spec.Command
	command.Dir = spec.WorkingDirectory
	command.Env = mergedEnvironment(command.Env, spec.Environment)
	if spec.Stdout != nil {
		command.Stdout = spec.Stdout
	}
	if spec.Stderr != nil {
		command.Stderr = spec.Stderr
	}
	configureProcessGroup(command)
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("启动任务进程：%w", err)
	}
	return &commandProcess{command: command}, nil
}

type commandProcess struct {
	command *exec.Cmd
}

func (p *commandProcess) Wait() (int, error) {
	err := p.command.Wait()
	if p.command.ProcessState == nil {
		return -1, err
	}
	return p.command.ProcessState.ExitCode(), err
}

func (p *commandProcess) Terminate() error {
	if p.command.Process == nil {
		return errorsProcessNotStarted
	}
	return terminateProcessGroup(p.command.Process)
}

func (p *commandProcess) KillGroup() error {
	if p.command.Process == nil {
		return errorsProcessNotStarted
	}
	return killProcessGroup(p.command.Process)
}

var errorsProcessNotStarted = fmt.Errorf("任务进程尚未启动")

func mergedEnvironment(base []string, additions map[string]string) []string {
	if base == nil {
		base = os.Environ()
	}
	result := append([]string(nil), base...)
	keys := make([]string, 0, len(additions))
	for key := range additions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+additions[key])
	}
	return result
}
