//go:build darwin || freebsd || openbsd || netbsd || dragonfly

package executor

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessGroup(process *os.Process) error {
	return syscall.Kill(-process.Pid, syscall.SIGTERM)
}

func killProcessGroup(process *os.Process) error {
	return syscall.Kill(-process.Pid, syscall.SIGKILL)
}
