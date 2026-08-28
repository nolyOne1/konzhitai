package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"time"
)

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
	process, err := l.processes.Start(ctx, systemdSpec)
	if err != nil {
		return nil, err
	}
	return &systemdProcess{Process: process, unitName: "yunling-run-" + spec.RunID}, nil
}

type systemdProcess struct {
	Process
	unitName string
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
	if spec.Command == nil || len(spec.Command.Args) == 0 {
		return nil, ErrInvalidAssignment
	}
	cpuQuota := strconv.FormatFloat(float64(spec.Resources.CPUMillicores)/10, 'f', -1, 64) + "%"
	timeoutSeconds := int64((spec.Timeout + time.Second - 1) / time.Second)
	arguments := []string{
		"--unit=yunling-run-" + spec.RunID,
		"--uid=yunling-runner",
		"--wait",
		"--collect",
		"--quiet",
		"--property=CPUQuota=" + cpuQuota,
		"--property=MemoryMax=" + strconv.FormatInt(spec.Resources.MemoryBytes, 10),
		"--property=TasksMax=" + strconv.Itoa(spec.Resources.TasksMax),
		"--property=RuntimeMaxSec=" + strconv.FormatInt(timeoutSeconds, 10),
		"--property=NoNewPrivileges=yes",
		"--property=PrivateTmp=yes",
		"--property=PrivateDevices=yes",
		"--property=ProtectSystem=strict",
		"--property=ProtectHome=yes",
		"--property=ProtectKernelTunables=yes",
		"--property=ProtectKernelModules=yes",
		"--property=ProtectControlGroups=yes",
		"--property=RestrictSUIDSGID=yes",
		"--property=LockPersonality=yes",
		"--property=ReadWritePaths=" + spec.WorkingDirectory,
		"--working-directory=" + spec.WorkingDirectory,
	}
	keys := make([]string, 0, len(spec.Environment))
	for key := range spec.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		arguments = append(arguments, "--setenv="+key+"="+spec.Environment[key])
	}
	arguments = append(arguments, "--")
	arguments = append(arguments, spec.Command.Args...)
	command := exec.Command("systemd-run", arguments...)
	command.Path = "systemd-run"
	if timeoutSeconds <= 0 {
		return nil, fmt.Errorf("%w：执行超时必须大于零", ErrInvalidAssignment)
	}
	return command, nil
}

func buildSystemdKillCommand(unitName, signal string) *exec.Cmd {
	command := exec.Command("systemctl", "kill", "--kill-who=all", "--signal="+signal, unitName)
	command.Path = "systemctl"
	return command
}
