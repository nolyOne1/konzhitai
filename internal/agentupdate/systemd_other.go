//go:build !linux

package agentupdate

import (
	"context"
	"errors"
)

type unsupportedSystem struct{}

func NewSystemController() SystemController { return unsupportedSystem{} }
func NewSystemdStarter() ApplyStarter       { return unsupportedSystem{} }
func (unsupportedSystem) DaemonReload(context.Context) error {
	return errors.New("当前系统不支持 systemd")
}
func (unsupportedSystem) RestartAgent(context.Context) error {
	return errors.New("当前系统不支持 systemd")
}
func (unsupportedSystem) WaitForConfirmation(context.Context, string) error {
	return errors.New("当前系统不支持 systemd")
}
func (unsupportedSystem) StartUpgrade(context.Context, string) error {
	return errors.New("当前系统不支持 systemd")
}
