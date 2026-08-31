package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const commandOutputLimit = 4096

var (
	ErrCommandNotAllowed = errors.New("备份命令不在白名单")
	ErrCommandTimedOut   = errors.New("备份命令执行超时")
	ErrCommandFailed     = errors.New("备份命令执行失败")
)

var pinnedCommands = map[string]string{
	"/usr/bin/pg_dump":    "/usr/bin/pg_dump",
	"/usr/bin/pg_restore": "/usr/bin/pg_restore",
	"/usr/bin/psql":       "/usr/bin/psql",
	"/usr/bin/mc":         "/usr/bin/mc",
	"/usr/bin/restic":     "/usr/bin/restic",
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type CommandExecutor interface {
	Run(context.Context, string, []string, map[string]string) (CommandResult, error)
}

type CommandRunner struct {
	timeout  time.Duration
	commands map[string]string
}

func NewCommandRunner(timeout time.Duration) *CommandRunner {
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	commands := make(map[string]string, len(pinnedCommands))
	for name, path := range pinnedCommands {
		commands[name] = path
	}
	return &CommandRunner{timeout: timeout, commands: commands}
}

func AllowedCommands() []string {
	return []string{
		"/usr/bin/pg_dump",
		"/usr/bin/pg_restore",
		"/usr/bin/psql",
		"/usr/bin/mc",
		"/usr/bin/restic",
	}
}

func (r *CommandRunner) Run(ctx context.Context, name string, args []string, environment map[string]string) (CommandResult, error) {
	if r == nil || r.commands == nil {
		return CommandResult{ExitCode: -1}, ErrCommandNotAllowed
	}
	executable, allowed := r.commands[name]
	if !allowed || name == "" || !strings.HasPrefix(name, "/") {
		return CommandResult{ExitCode: -1}, ErrCommandNotAllowed
	}
	runContext, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	command := exec.Command(executable, args...)
	configureProcessGroup(command)
	command.Env = append([]string{}, os.Environ()...)
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(environment[key], '\x00') {
			return CommandResult{ExitCode: -1}, ErrInvalidRequest
		}
		command.Env = append(command.Env, key+"="+environment[key])
	}
	stdout := &boundedBuffer{maximum: commandOutputLimit}
	stderr := &boundedBuffer{maximum: commandOutputLimit}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1}, fmt.Errorf("%w：无法启动", ErrCommandFailed)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var runErr error
	select {
	case runErr = <-done:
	case <-runContext.Done():
		killProcessGroup(command)
		<-done
		result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1}
		return result, ErrCommandTimedOut
	}
	exitCode := 0
	if runErr != nil {
		exitCode = -1
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}
	if runErr != nil {
		return result, fmt.Errorf("%w（退出码 %d）", ErrCommandFailed, exitCode)
	}
	return result, nil
}

type boundedBuffer struct {
	mutex   sync.Mutex
	data    []byte
	maximum int
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	remaining := b.maximum - len(b.data)
	if remaining > 0 {
		if len(value) < remaining {
			remaining = len(value)
		}
		b.data = append(b.data, value[:remaining]...)
	}
	return len(value), nil
}

func (b *boundedBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return string(append([]byte(nil), b.data...))
}

var _ CommandExecutor = (*CommandRunner)(nil)
