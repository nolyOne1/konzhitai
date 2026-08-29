package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	systemdSpecFileName   = "systemd-run-spec.json"
	systemdStdoutFileName = "stdout.log"
	systemdStderrFileName = "stderr.log"
)

type systemdRunSpec struct {
	Arguments        []string          `json:"arguments"`
	Environment      map[string]string `json:"environment"`
	WorkingDirectory string            `json:"working_directory"`
}

type SystemdLauncher struct {
	processes *ProcessLauncher
}

func NewSystemdLauncher() *SystemdLauncher {
	return &SystemdLauncher{processes: NewProcessLauncher()}
}

func (l *SystemdLauncher) Start(ctx context.Context, spec LaunchSpec) (Process, error) {
	command, err := buildSystemdCommand(spec)
	if err != nil {
		return nil, err
	}
	systemdSpec := spec
	systemdSpec.Command = command
	systemdSpec.Environment = nil
	// 任务输出由固定模板写入每次运行的独立文件，systemctl 本身的输出不是业务日志。
	systemdSpec.Stdout = nil
	systemdSpec.Stderr = nil
	process, err := l.processes.Start(ctx, systemdSpec)
	if err != nil {
		_ = os.Remove(filepath.Join(spec.WorkingDirectory, systemdSpecFileName))
		return nil, err
	}
	return &systemdProcess{
		Process:    process,
		unitName:   systemdUnitName(spec.RunID),
		specPath:   filepath.Join(spec.WorkingDirectory, systemdSpecFileName),
		stdoutPath: filepath.Join(spec.WorkingDirectory, systemdStdoutFileName),
		stderrPath: filepath.Join(spec.WorkingDirectory, systemdStderrFileName),
		stdout:     spec.Stdout,
		stderr:     spec.Stderr,
	}, nil
}

type systemdProcess struct {
	Process
	unitName               string
	specPath               string
	stdoutPath, stderrPath string
	stdout, stderr         io.Writer
}

func (p *systemdProcess) Wait() (int, error) {
	finished := make(chan processResult, 1)
	go func() {
		exitCode, err := p.Process.Wait()
		finished <- processResult{exitCode: exitCode, err: err}
	}()
	stdoutTail := systemdLogTail{path: p.stdoutPath, destination: p.stdout}
	stderrTail := systemdLogTail{path: p.stderrPath, destination: p.stderr}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var streamErr error
	for {
		select {
		case result := <-finished:
			streamErr = errors.Join(streamErr, stdoutTail.copyAvailable(), stderrTail.copyAvailable())
			removeErr := os.Remove(p.specPath)
			if errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
			}
			_ = os.Remove(p.stdoutPath)
			_ = os.Remove(p.stderrPath)
			return result.exitCode, errors.Join(result.err, streamErr, removeErr)
		case <-ticker.C:
			streamErr = errors.Join(streamErr, stdoutTail.copyAvailable(), stderrTail.copyAvailable())
		}
	}
}

func (p *systemdProcess) Terminate() error {
	if err := buildSystemdKillCommand(p.unitName, "TERM").Run(); err != nil {
		return errors.Join(err, p.Process.Terminate())
	}
	return nil
}

func (p *systemdProcess) KillGroup() error {
	if err := buildSystemdKillCommand(p.unitName, "KILL").Run(); err != nil {
		return errors.Join(err, p.Process.KillGroup())
	}
	return nil
}

func buildSystemdCommand(spec LaunchSpec) (*exec.Cmd, error) {
	if spec.Command == nil || len(spec.Command.Args) == 0 || spec.WorkingDirectory == "" || spec.Timeout <= 0 {
		return nil, fmt.Errorf("%w：systemd 执行规格不完整", ErrInvalidAssignment)
	}
	stored := systemdRunSpec{
		Arguments:        append([]string(nil), spec.Command.Args...),
		Environment:      cloneEnvironment(spec.Environment),
		WorkingDirectory: spec.WorkingDirectory,
	}
	body, err := json.Marshal(stored)
	if err != nil {
		return nil, fmt.Errorf("编码 systemd 执行规格：%w", err)
	}
	if err := os.WriteFile(filepath.Join(spec.WorkingDirectory, systemdSpecFileName), body, 0o640); err != nil {
		return nil, fmt.Errorf("写入 systemd 执行规格：%w", err)
	}
	for _, name := range []string{systemdStdoutFileName, systemdStderrFileName} {
		file, fileErr := os.OpenFile(filepath.Join(spec.WorkingDirectory, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o660)
		if fileErr != nil {
			return nil, fmt.Errorf("准备 systemd 日志文件：%w", fileErr)
		}
		if fileErr = file.Close(); fileErr != nil {
			return nil, fmt.Errorf("关闭 systemd 日志文件：%w", fileErr)
		}
	}
	command := exec.Command("systemctl", "start", "--wait", "--no-ask-password", systemdUnitName(spec.RunID))
	command.Path = "systemctl"
	return command, nil
}

func systemdUnitName(runID string) string {
	return "yunling-run@" + runID + ".service"
}

type systemdLogTail struct {
	path        string
	destination io.Writer
	offset      int64
}

func (tail *systemdLogTail) copyAvailable() error {
	if tail.destination == nil {
		return nil
	}
	file, err := os.Open(tail.path)
	if err != nil {
		return fmt.Errorf("打开任务日志 %q：%w", tail.path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("读取任务日志状态 %q：%w", tail.path, err)
	}
	if info.Size() < tail.offset {
		tail.offset = 0
	}
	if _, err := file.Seek(tail.offset, io.SeekStart); err != nil {
		return fmt.Errorf("定位任务日志 %q：%w", tail.path, err)
	}
	written, err := io.Copy(tail.destination, file)
	tail.offset += written
	if err != nil {
		return fmt.Errorf("读取任务日志 %q：%w", tail.path, err)
	}
	return nil
}

func buildSystemdKillCommand(unitName, signal string) *exec.Cmd {
	command := exec.Command("systemctl", "kill", "--kill-who=all", "--signal="+signal, unitName)
	command.Path = "systemctl"
	return command
}

// RunSystemdSpec 只由 root 管理的 yunling-run@.service 模板调用；模板负责固定执行账户与安全边界。
func RunSystemdSpec(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return -1, fmt.Errorf("打开任务执行规格：%w", err)
	}
	var spec systemdRunSpec
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decodeErr := decoder.Decode(&spec)
	closeErr := file.Close()
	if decodeErr != nil {
		_ = os.Remove(path)
		return -1, fmt.Errorf("解析任务执行规格：%w", decodeErr)
	}
	if closeErr != nil {
		return -1, fmt.Errorf("关闭任务执行规格：%w", closeErr)
	}
	if err := os.Remove(path); err != nil {
		return -1, fmt.Errorf("清除任务执行规格：%w", err)
	}
	if len(spec.Arguments) == 0 || spec.WorkingDirectory == "" {
		return -1, fmt.Errorf("%w：任务执行规格缺少命令或工作目录", ErrInvalidAssignment)
	}
	command := exec.Command(spec.Arguments[0], spec.Arguments[1:]...)
	command.Dir = spec.WorkingDirectory
	command.Env = mergedEnvironment(nil, spec.Environment)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return exitError.ExitCode(), err
		}
		return -1, err
	}
	return 0, nil
}
