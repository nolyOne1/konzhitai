//go:build windows

package backup

import "os/exec"

func configureProcessGroup(_ *exec.Cmd) {}

func killProcessGroup(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}
