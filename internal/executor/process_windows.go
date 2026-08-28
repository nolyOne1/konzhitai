//go:build windows

package executor

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func terminateProcessGroup(process *os.Process) error {
	return runTaskKill(process.Pid, false)
}

func killProcessGroup(process *os.Process) error {
	return errors.Join(runTaskKill(process.Pid, true), process.Kill())
}

func runTaskKill(processID int, force bool) error {
	arguments := []string{"/PID", strconv.Itoa(processID), "/T"}
	if force {
		arguments = append(arguments, "/F")
	}
	return exec.Command("taskkill", arguments...).Run()
}
