package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/agentupdate"
)

type UpgradeManager interface {
	Stage(context.Context, agentprotocol.UpgradeCommand) (agentupdate.Spec, error)
	StartApply(context.Context, string) error
	Rollback(context.Context, agentprotocol.UpgradeCommand) error
	RuntimeState() (*agentprotocol.UpgradeRuntimeState, error)
	SaveRuntimeState(agentprotocol.UpgradeRuntimeState) error
}

type UpgradeTransport interface {
	ReceiveUpgradeCommand(context.Context) (agentprotocol.UpgradeCommand, error)
	SendUpgradeEvent(context.Context, agentprotocol.UpgradeEvent) error
}

type UpgradeClient struct {
	manager   UpgradeManager
	transport UpgradeTransport
	now       func() time.Time
	seen      map[string]bool
}

func NewUpgradeClient(manager UpgradeManager, transport UpgradeTransport, now func() time.Time) *UpgradeClient {
	if now == nil {
		now = time.Now
	}
	return &UpgradeClient{manager: manager, transport: transport, now: now, seen: map[string]bool{}}
}

func (c *UpgradeClient) Run(ctx context.Context) error {
	for {
		command, err := c.transport.ReceiveUpgradeCommand(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("接收代理升级命令：%w", err)
		}
		if err := c.handle(ctx, command); err != nil {
			return err
		}
	}
}

func (c *UpgradeClient) handle(ctx context.Context, command agentprotocol.UpgradeCommand) error {
	current, err := c.manager.RuntimeState()
	if err != nil {
		return fmt.Errorf("读取本地升级状态：%w", err)
	}
	if current != nil && current.CommandID == command.CommandID {
		if err := c.transport.SendUpgradeEvent(ctx, eventFromRuntime(*current, c.now())); err != nil {
			return err
		}
		if c.seen[command.CommandID] {
			return nil
		}
		c.seen[command.CommandID] = true
		switch current.Stage {
		case agentprotocol.StageAccepted, agentprotocol.StageDownloading, agentprotocol.StageVerifying, agentprotocol.StageInstalling:
			return c.resumeInstall(ctx, command, current.Stage)
		case agentprotocol.StageRollingBack:
			if err := c.manager.Rollback(ctx, command); err != nil {
				if errors.Is(err, agentupdate.ErrNoRollbackNeeded) {
					return c.transition(ctx, command, agentprotocol.StageRolledBack, "", "升级未替换文件，已安全结束")
				}
				return c.fail(ctx, command, "rollback_failed", "代理回滚恢复失败："+err.Error())
			}
		}
		return nil
	}
	c.seen[command.CommandID] = true
	if err := c.transition(ctx, command, agentprotocol.StageAccepted, "", ""); err != nil {
		return err
	}
	if command.Action == agentprotocol.UpgradeRollback {
		if err := c.transition(ctx, command, agentprotocol.StageRollingBack, "", ""); err != nil {
			return err
		}
		if err := c.manager.Rollback(ctx, command); err != nil {
			if errors.Is(err, agentupdate.ErrNoRollbackNeeded) {
				return c.transition(ctx, command, agentprotocol.StageRolledBack, "", "升级未替换文件，已安全结束")
			}
			return c.fail(ctx, command, "rollback_failed", "代理回滚启动失败："+err.Error())
		}
		return nil
	}
	return c.resumeInstall(ctx, command, agentprotocol.StageAccepted)
}

func (c *UpgradeClient) resumeInstall(ctx context.Context, command agentprotocol.UpgradeCommand, stage agentprotocol.UpgradeStage) error {
	if stage == agentprotocol.StageAccepted {
		if err := c.transition(ctx, command, agentprotocol.StageDownloading, "", ""); err != nil {
			return err
		}
		stage = agentprotocol.StageDownloading
	}
	if stage == agentprotocol.StageDownloading {
		if _, err := c.manager.Stage(ctx, command); err != nil {
			return c.fail(ctx, command, "stage_failed", "代理升级暂存失败："+err.Error())
		}
		if err := c.transition(ctx, command, agentprotocol.StageVerifying, "", ""); err != nil {
			return err
		}
		stage = agentprotocol.StageVerifying
	}
	if stage == agentprotocol.StageVerifying {
		if err := c.transition(ctx, command, agentprotocol.StageInstalling, "", ""); err != nil {
			return err
		}
		stage = agentprotocol.StageInstalling
	}
	if stage != agentprotocol.StageInstalling {
		return nil
	}
	if err := c.manager.StartApply(ctx, command.CommandID); err != nil {
		if errors.Is(err, agentupdate.ErrRollbackFailed) {
			return c.fail(ctx, command, "rollback_failed", "代理升级失败且本地恢复未完成："+err.Error())
		}
		return c.fail(ctx, command, "apply_start_failed", "代理升级安装启动失败："+err.Error())
	}
	return nil
}

func (c *UpgradeClient) fail(ctx context.Context, command agentprotocol.UpgradeCommand, code, message string) error {
	return c.transition(ctx, command, agentprotocol.StageFailed, code, message)
}

func (c *UpgradeClient) transition(ctx context.Context, command agentprotocol.UpgradeCommand, stage agentprotocol.UpgradeStage, code, message string) error {
	now := c.now().UTC()
	state := agentprotocol.UpgradeRuntimeState{
		CommandID: command.CommandID, TargetID: command.TargetID,
		TargetVersion: command.TargetVersion, Stage: stage, UpdatedAt: now,
		ErrorCode: code, Message: message,
	}
	if err := c.manager.SaveRuntimeState(state); err != nil {
		return fmt.Errorf("保存本地升级状态：%w", err)
	}
	if err := c.transport.SendUpgradeEvent(ctx, eventFromRuntime(state, now)); err != nil {
		return fmt.Errorf("上报代理升级阶段：%w", err)
	}
	return nil
}

func eventFromRuntime(state agentprotocol.UpgradeRuntimeState, occurredAt time.Time) agentprotocol.UpgradeEvent {
	return agentprotocol.UpgradeEvent{
		CommandID: state.CommandID, TargetID: state.TargetID, Stage: state.Stage,
		OccurredAt: occurredAt.UTC(), ErrorCode: state.ErrorCode, Message: state.Message,
	}
}
