package agent

import (
	"context"
	"fmt"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/executor"
)

type ExecutionRunner interface {
	Start(context.Context, agentprotocol.Assignment) (<-chan executor.Event, error)
	Cancel(context.Context, string, string) error
}

type ExecutionTransport interface {
	ReceiveExecutionCommand(context.Context) (agentprotocol.ExecutionCommand, error)
	SendRunEvent(context.Context, agentprotocol.RunEvent) error
}

type ExecutionClient struct {
	runner    ExecutionRunner
	transport ExecutionTransport
}

func NewExecutionClient(runner ExecutionRunner, transport ExecutionTransport) *ExecutionClient {
	return &ExecutionClient{runner: runner, transport: transport}
}

func (c *ExecutionClient) Run(ctx context.Context) error {
	commands := make(chan agentprotocol.ExecutionCommand)
	receiveErrors := make(chan error, 1)
	forwardErrors := make(chan error, 1)
	go func() {
		for {
			command, err := c.transport.ReceiveExecutionCommand(ctx)
			if err != nil {
				select {
				case receiveErrors <- err:
				case <-ctx.Done():
				}
				return
			}
			select {
			case commands <- command:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-receiveErrors:
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("接收任务执行命令：%w", err)
		case err := <-forwardErrors:
			return err
		case command := <-commands:
			switch command.Type {
			case agentprotocol.CommandAssign:
				if command.Assignment == nil {
					return fmt.Errorf("任务分配命令缺少执行信息")
				}
				events, err := c.runner.Start(ctx, *command.Assignment)
				if err != nil {
					report := agentprotocol.RunEvent{
						RunID:          command.Assignment.RunID,
						ExecutionToken: command.Assignment.ExecutionToken,
						Sequence:       1,
						Type:           string(executor.EventFailed),
						OccurredAt:     time.Now().UTC(),
						ExitCode:       -1,
						Message:        "任务启动失败：" + err.Error(),
					}
					if sendErr := c.transport.SendRunEvent(ctx, report); sendErr != nil {
						return fmt.Errorf("上报任务启动失败：%w", sendErr)
					}
					continue
				}
				go c.forwardEvents(ctx, *command.Assignment, events, forwardErrors)
			case agentprotocol.CommandCancel:
				if command.Cancellation == nil {
					return fmt.Errorf("任务取消命令缺少执行身份")
				}
				if err := c.runner.Cancel(ctx, command.Cancellation.RunID, command.Cancellation.ExecutionToken); err != nil {
					return fmt.Errorf("取消运行任务：%w", err)
				}
			default:
				return fmt.Errorf("不支持的任务执行命令：%s", command.Type)
			}
		}
	}
}

func (c *ExecutionClient) forwardEvents(
	ctx context.Context,
	assignment agentprotocol.Assignment,
	events <-chan executor.Event,
	errors chan<- error,
) {
	for event := range events {
		report := agentprotocol.RunEvent{
			RunID:          assignment.RunID,
			ExecutionToken: assignment.ExecutionToken,
			Sequence:       event.Sequence,
			Type:           string(event.Type),
			OccurredAt:     event.OccurredAt,
			ExitCode:       event.ExitCode,
			Message:        event.Message,
		}
		if err := c.transport.SendRunEvent(ctx, report); err != nil {
			select {
			case errors <- fmt.Errorf("上报任务运行事件：%w", err):
			case <-ctx.Done():
			}
			return
		}
	}
}
