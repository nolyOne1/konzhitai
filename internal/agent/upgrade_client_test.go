package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/agentupdate"
)

func TestUpgradeClientStagesReportsAndStartsInstall(t *testing.T) {
	manager := &fakeUpgradeManager{}
	transport, cancel := upgradeTransportWithCommands(validUpgradeCommand("upgrade-1"))
	transport.cancelAt = 4
	transport.cancel = cancel
	client := NewUpgradeClient(manager, transport, fixedUpgradeNow)
	if err := client.Run(transport.ctx); err != nil {
		t.Fatal(err)
	}
	if manager.stageCalls != 1 || manager.startCalls != 1 {
		t.Fatalf("升级处理次数错误：stage=%d start=%d", manager.stageCalls, manager.startCalls)
	}
	assertUpgradeStages(t, transport.events, "accepted", "downloading", "verifying", "installing")
}

func TestUpgradeClientDoesNotRestageDuplicateCommand(t *testing.T) {
	manager := &fakeUpgradeManager{}
	command := validUpgradeCommand("upgrade-1")
	transport, cancel := upgradeTransportWithCommands(command, command)
	transport.cancelAt = 5
	transport.cancel = cancel
	client := NewUpgradeClient(manager, transport, fixedUpgradeNow)
	if err := client.Run(transport.ctx); err != nil {
		t.Fatal(err)
	}
	if manager.stageCalls != 1 || manager.startCalls != 1 {
		t.Fatalf("重复命令发生重复处理：stage=%d start=%d", manager.stageCalls, manager.startCalls)
	}
}

func TestUpgradeClientResumesPersistedDownloadAfterRestart(t *testing.T) {
	command := validUpgradeCommand("upgrade-1")
	manager := &fakeUpgradeManager{state: &agentprotocol.UpgradeRuntimeState{CommandID: command.CommandID, TargetID: command.TargetID, TargetVersion: command.TargetVersion, Stage: agentprotocol.StageDownloading}}
	transport, cancel := upgradeTransportWithCommands(command)
	transport.cancelAt = 3
	transport.cancel = cancel
	if err := NewUpgradeClient(manager, transport, fixedUpgradeNow).Run(transport.ctx); err != nil {
		t.Fatal(err)
	}
	if manager.stageCalls != 1 || manager.startCalls != 1 {
		t.Fatalf("重启后未从下载阶段恢复：stage=%d start=%d", manager.stageCalls, manager.startCalls)
	}
	assertUpgradeStages(t, transport.events, "downloading", "verifying", "installing")
}

func TestUpgradeClientStartsRollback(t *testing.T) {
	manager := &fakeUpgradeManager{}
	command := validUpgradeCommand("rollback-1")
	command.Action = agentprotocol.UpgradeRollback
	transport, cancel := upgradeTransportWithCommands(command)
	transport.cancelAt = 2
	transport.cancel = cancel
	client := NewUpgradeClient(manager, transport, fixedUpgradeNow)
	if err := client.Run(transport.ctx); err != nil {
		t.Fatal(err)
	}
	if manager.rollbackCalls != 1 {
		t.Fatalf("回滚命令未执行：%d", manager.rollbackCalls)
	}
	assertUpgradeStages(t, transport.events, "accepted", "rolling_back")
}

func TestUpgradeClientReportsRolledBackWhenFilesWereNotReplaced(t *testing.T) {
	manager := &fakeUpgradeManager{rollbackErr: agentupdate.ErrNoRollbackNeeded}
	command := validUpgradeCommand("rollback-1")
	command.Action = agentprotocol.UpgradeRollback
	transport, cancel := upgradeTransportWithCommands(command)
	transport.cancelAt = 3
	transport.cancel = cancel
	if err := NewUpgradeClient(manager, transport, fixedUpgradeNow).Run(transport.ctx); err != nil {
		t.Fatal(err)
	}
	assertUpgradeStages(t, transport.events, "accepted", "rolling_back", "rolled_back")
}

func TestUpgradeClientReportsStageFailure(t *testing.T) {
	manager := &fakeUpgradeManager{stageErr: errors.New("下载中断")}
	transport, cancel := upgradeTransportWithCommands(validUpgradeCommand("upgrade-1"))
	transport.cancelAt = 3
	transport.cancel = cancel
	client := NewUpgradeClient(manager, transport, fixedUpgradeNow)
	if err := client.Run(transport.ctx); err != nil {
		t.Fatal(err)
	}
	last := transport.events[len(transport.events)-1]
	if last.Stage != agentprotocol.StageFailed || last.ErrorCode != "stage_failed" || last.Message != "代理升级暂存失败：下载中断" {
		t.Fatalf("失败事件不完整：%+v", last)
	}
}

type fakeUpgradeManager struct {
	stageCalls, startCalls, rollbackCalls int
	stageErr                              error
	rollbackErr                           error
	state                                 *agentprotocol.UpgradeRuntimeState
}

func (m *fakeUpgradeManager) Stage(context.Context, agentprotocol.UpgradeCommand) (agentupdate.Spec, error) {
	m.stageCalls++
	return agentupdate.Spec{}, m.stageErr
}
func (m *fakeUpgradeManager) StartApply(context.Context, string) error { m.startCalls++; return nil }
func (m *fakeUpgradeManager) Rollback(context.Context, agentprotocol.UpgradeCommand) error {
	m.rollbackCalls++
	return m.rollbackErr
}
func (m *fakeUpgradeManager) RuntimeState() (*agentprotocol.UpgradeRuntimeState, error) {
	if m.state == nil {
		return nil, nil
	}
	copy := *m.state
	return &copy, nil
}
func (m *fakeUpgradeManager) SaveRuntimeState(state agentprotocol.UpgradeRuntimeState) error {
	copy := state
	m.state = &copy
	return nil
}

type fakeUpgradeTransport struct {
	ctx      context.Context
	commands chan agentprotocol.UpgradeCommand
	mu       sync.Mutex
	events   []agentprotocol.UpgradeEvent
	cancelAt int
	cancel   context.CancelFunc
}

func upgradeTransportWithCommands(commands ...agentprotocol.UpgradeCommand) (*fakeUpgradeTransport, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	transport := &fakeUpgradeTransport{ctx: ctx, commands: make(chan agentprotocol.UpgradeCommand, len(commands))}
	for _, command := range commands {
		transport.commands <- command
	}
	return transport, cancel
}
func (t *fakeUpgradeTransport) ReceiveUpgradeCommand(ctx context.Context) (agentprotocol.UpgradeCommand, error) {
	select {
	case command := <-t.commands:
		return command, nil
	case <-ctx.Done():
		return agentprotocol.UpgradeCommand{}, ctx.Err()
	}
}
func (t *fakeUpgradeTransport) SendUpgradeEvent(_ context.Context, event agentprotocol.UpgradeEvent) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
	if t.cancelAt > 0 && len(t.events) >= t.cancelAt {
		t.cancel()
	}
	return nil
}

func validUpgradeCommand(id string) agentprotocol.UpgradeCommand {
	return agentprotocol.UpgradeCommand{CommandID: id, TargetID: "target-1", Action: agentprotocol.UpgradeInstall, SourceVersion: "0.1.0", TargetVersion: "0.2.0"}
}
func fixedUpgradeNow() time.Time { return time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC) }
func assertUpgradeStages(t *testing.T, events []agentprotocol.UpgradeEvent, stages ...string) {
	t.Helper()
	if len(events) != len(stages) {
		t.Fatalf("阶段事件数量：got=%d want=%d events=%+v", len(events), len(stages), events)
	}
	for index, stage := range stages {
		if string(events[index].Stage) != stage {
			t.Fatalf("第 %d 个阶段：got=%s want=%s", index, events[index].Stage, stage)
		}
	}
}
