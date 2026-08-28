package executor

import (
	"errors"
	"os/exec"
)

var ErrRuntimeNotAllowed = errors.New("运行环境未被允许")

var supportedRuntimes = map[string]string{
	"bash":       "bash",
	"node":       "node",
	"powershell": "powershell",
	"python3":    "python3",
}

func BuildCommand(runtimeName, scriptPath string, arguments []string) (*exec.Cmd, error) {
	executable, allowed := supportedRuntimes[runtimeName]
	if !allowed {
		return nil, ErrRuntimeNotAllowed
	}
	commandArguments := make([]string, 0, len(arguments)+4)
	if runtimeName == "powershell" {
		commandArguments = append(commandArguments, "-NoProfile", "-NonInteractive", "-File")
	}
	commandArguments = append(commandArguments, scriptPath)
	commandArguments = append(commandArguments, arguments...)
	command := exec.Command(executable, commandArguments...)
	command.Path = executable
	return command, nil
}
