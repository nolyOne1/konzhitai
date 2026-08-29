package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/executor"
)

func TestExecutionClientForwardsRunnerEventsWithExecutionIdentity(t *testing.T) {
	transport := &fakeExecutionTransport{
		commands: make(chan agentprotocol.ExecutionCommand, 1),
		events:   make(chan agentprotocol.RunEvent, 2),
	}
	runner := &scriptedExecutionRunner{}
	client := NewExecutionClient(runner, transport)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	transport.commands <- agentprotocol.ExecutionCommand{
		Type: agentprotocol.CommandAssign,
		Assignment: &agentprotocol.Assignment{
			RunID: "run-1", ExecutionToken: "token-1", ScriptVersionID: "version-1", Runtime: "python3",
		},
	}

	first := receiveRunEvent(t, transport.events)
	second := receiveRunEvent(t, transport.events)
	if first.RunID != "run-1" || first.ExecutionToken != "token-1" || first.Sequence != 1 || first.Type != string(executor.EventStarted) {
		t.Fatalf("开始事件身份或内容不完整：%+v", first)
	}
	if second.Sequence != 2 || second.Type != string(executor.EventSucceeded) || second.ExitCode != 0 {
		t.Fatalf("终态事件未按序转发：%+v", second)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消执行客户端应正常退出：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("执行客户端未在 1 秒内退出")
	}
}

func TestExecutionClientReportsStartFailureAndContinuesReceiving(t *testing.T) {
	transport := &fakeExecutionTransport{
		commands: make(chan agentprotocol.ExecutionCommand, 2),
		events:   make(chan agentprotocol.RunEvent, 3),
	}
	runner := &failsOnceExecutionRunner{err: errors.New("脚本版本尚未同步")}
	client := NewExecutionClient(runner, transport)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	transport.commands <- assignmentCommand("run-1", "token-1")

	failed := receiveRunEvent(t, transport.events)
	if failed.RunID != "run-1" || failed.ExecutionToken != "token-1" || failed.Sequence != 1 || failed.Type != string(executor.EventFailed) || failed.Message == "" {
		t.Fatalf("启动失败必须带执行身份上报：%+v", failed)
	}
	transport.commands <- assignmentCommand("run-2", "token-2")
	started := receiveRunEvent(t, transport.events)
	succeeded := receiveRunEvent(t, transport.events)
	if started.RunID != "run-2" || succeeded.RunID != "run-2" || succeeded.Type != string(executor.EventSucceeded) {
		t.Fatalf("一次启动失败后仍应继续接收后续任务：started=%+v succeeded=%+v", started, succeeded)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消执行客户端应正常退出：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("执行客户端未在 1 秒内退出")
	}
}

func TestExecutionClientIgnoresDuplicateAssignmentAndContinuesReceiving(t *testing.T) {
	transport := &fakeExecutionTransport{
		commands: make(chan agentprotocol.ExecutionCommand, 2),
		events:   make(chan agentprotocol.RunEvent, 2),
	}
	runner := &failsOnceExecutionRunner{err: executor.ErrRunAlreadyActive}
	client := NewExecutionClient(runner, transport)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	transport.commands <- assignmentCommand("run-1", "token-1")
	transport.commands <- assignmentCommand("run-2", "token-2")
	started := receiveRunEvent(t, transport.events)
	succeeded := receiveRunEvent(t, transport.events)
	if started.RunID != "run-2" || started.Type != string(executor.EventStarted) || succeeded.RunID != "run-2" || succeeded.Type != string(executor.EventSucceeded) {
		t.Fatalf("重复派发不得产生失败事件且后续任务必须继续：started=%+v succeeded=%+v", started, succeeded)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消执行客户端应正常退出：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("执行客户端未在 1 秒内退出")
	}
}

func assignmentCommand(runID, token string) agentprotocol.ExecutionCommand {
	return agentprotocol.ExecutionCommand{
		Type: agentprotocol.CommandAssign,
		Assignment: &agentprotocol.Assignment{
			RunID: runID, ExecutionToken: token, ScriptVersionID: "version-1", Runtime: "python3",
		},
	}
}

func receiveRunEvent(t *testing.T, events <-chan agentprotocol.RunEvent) agentprotocol.RunEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("1 秒内未收到运行事件")
		return agentprotocol.RunEvent{}
	}
}

type scriptedExecutionRunner struct{}

func (r *scriptedExecutionRunner) Start(_ context.Context, _ agentprotocol.Assignment) (<-chan executor.Event, error) {
	events := make(chan executor.Event, 2)
	events <- executor.Event{Sequence: 1, Type: executor.EventStarted, OccurredAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC), Message: "任务已开始执行"}
	events <- executor.Event{Sequence: 2, Type: executor.EventSucceeded, OccurredAt: time.Date(2026, 8, 28, 12, 0, 1, 0, time.UTC), ExitCode: 0, Message: "任务执行成功"}
	close(events)
	return events, nil
}

type failsOnceExecutionRunner struct {
	err error
}

func (r *failsOnceExecutionRunner) Start(ctx context.Context, assignment agentprotocol.Assignment) (<-chan executor.Event, error) {
	if r.err != nil {
		err := r.err
		r.err = nil
		return nil, err
	}
	return (&scriptedExecutionRunner{}).Start(ctx, assignment)
}

func (r *failsOnceExecutionRunner) Cancel(context.Context, string, string) error {
	return nil
}

func (r *scriptedExecutionRunner) Cancel(context.Context, string, string) error {
	return nil
}

type fakeExecutionTransport struct {
	commands chan agentprotocol.ExecutionCommand
	events   chan agentprotocol.RunEvent
}

func (t *fakeExecutionTransport) ReceiveExecutionCommand(ctx context.Context) (agentprotocol.ExecutionCommand, error) {
	select {
	case command := <-t.commands:
		return command, nil
	case <-ctx.Done():
		return agentprotocol.ExecutionCommand{}, ctx.Err()
	}
}

func (t *fakeExecutionTransport) SendRunEvent(_ context.Context, event agentprotocol.RunEvent) error {
	t.events <- event
	return nil
}
