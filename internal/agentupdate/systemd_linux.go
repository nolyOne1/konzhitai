//go:build linux

package agentupdate

import (
	"context"
	"os"
	"os/exec"
	"time"
)

type systemdController struct{}
type systemdStarter struct{}

func NewSystemController() SystemController { return systemdController{} }
func NewSystemdStarter() ApplyStarter       { return systemdStarter{} }
func (systemdController) DaemonReload(ctx context.Context) error {
	return exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
}
func (systemdController) RestartAgent(ctx context.Context) error {
	return exec.CommandContext(ctx, "systemctl", "restart", "yunling-agent.service").Run()
}
func (systemdController) WaitForConfirmation(ctx context.Context, path string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (systemdStarter) StartUpgrade(ctx context.Context, commandID string) error {
	if !validCommandID(commandID) {
		return ErrInvalidCommandID
	}
	return exec.CommandContext(ctx, "systemctl", "start", "yunling-agent-upgrade@"+commandID+".service").Run()
}
