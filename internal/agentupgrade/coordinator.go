package agentupgrade

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"yunling.local/platform/internal/agentprotocol"
)

type CoordinatorStore interface {
	ActivePlan(context.Context) (*Plan, error)
	Plan(context.Context, string) (Plan, error)
	SavePlan(context.Context, Plan) (Plan, error)
	Release(context.Context, string) (ReleaseInfo, error)
	ServerRuntime(context.Context, string) (ServerRuntime, error)
	SetServerDraining(context.Context, string, bool) error
	AppendEvent(context.Context, Event) error
}

type UpgradeCommandSender interface {
	SendUpgradeCommand(context.Context, string, agentprotocol.UpgradeCommand) error
}

type Coordinator struct {
	store  CoordinatorStore
	sender UpgradeCommandSender
	now    func() time.Time
	newID  func() string
}

func NewCoordinator(store CoordinatorStore, sender UpgradeCommandSender, now func() time.Time) *Coordinator {
	if now == nil {
		now = time.Now
	}
	return &Coordinator{store: store, sender: sender, now: now, newID: uuid.NewString}
}

func (c *Coordinator) Scan(ctx context.Context) error {
	plan, err := c.store.ActivePlan(ctx)
	if err != nil || plan == nil {
		return err
	}
	if plan.Status != PlanRunning && plan.Status != PlanPending && plan.Status != PlanPaused {
		return nil
	}
	now := c.now().UTC()
	changed := false
	commands := []sentCommand{}
	restoreScheduling := []string{}
	mayStartTarget := (plan.Status == PlanRunning || plan.Status == PlanPending) && !plan.CancelRequested
	for index := range plan.Targets {
		target := &plan.Targets[index]
		if target.BatchNumber != plan.CurrentBatch {
			continue
		}
		switch target.Status {
		case TargetWaiting:
			if !mayStartTarget {
				continue
			}
			target.Status, target.StartedAt, target.UpdatedAt = TargetDraining, &now, now
			changed = true
		case TargetDraining:
			if !mayStartTarget && !plan.CancelRequested {
				continue
			}
			if target.StartedAt != nil && now.Sub(*target.StartedAt) > time.Duration(plan.DrainTimeoutSeconds)*time.Second {
				plan.Status, plan.PauseReason = PlanPaused, "服务器排空超时"
				changed = true
				continue
			}
			if err := c.store.SetServerDraining(ctx, target.ServerID, true); err != nil {
				return err
			}
			runtime, err := c.store.ServerRuntime(ctx, target.ServerID)
			if err != nil {
				return err
			}
			if !runtime.Enabled || (runtime.Status != "online" && runtime.Status != "draining") || runtime.RunningTasks > 0 {
				continue
			}
			release, err := c.store.Release(ctx, plan.TargetReleaseID)
			if err != nil {
				return err
			}
			artifact, ok := matchingArtifact(release.Artifacts, runtime.AgentOS, runtime.AgentArch)
			if !ok {
				return ErrArtifactUnavailable
			}
			target.Status, target.Attempts, target.UpdatedAt = TargetDownloading, target.Attempts+1, now
			commands = append(commands, sentCommand{target.ServerID, installCommand(*plan, *target, artifact)})
			changed = true
		case TargetDownloading:
			runtime, err := c.store.ServerRuntime(ctx, target.ServerID)
			if err != nil {
				return err
			}
			release, err := c.store.Release(ctx, plan.TargetReleaseID)
			if err != nil {
				return err
			}
			artifact, ok := matchingArtifact(release.Artifacts, runtime.AgentOS, runtime.AgentArch)
			if !ok {
				return ErrArtifactUnavailable
			}
			commands = append(commands, sentCommand{target.ServerID, installCommand(*plan, *target, artifact)})
		case TargetReconnecting:
			if now.Sub(target.UpdatedAt) >= time.Duration(plan.ReconnectTimeoutSeconds)*time.Second {
				commands = withoutBatchCommands(commands, *plan, target.BatchNumber)
				commands = append(commands, c.rollbackBatch(plan, target.BatchNumber, target.ID, "reconnect_timeout", now)...)
				changed = true
			}
		case TargetHealthChecking:
			runtime, err := c.store.ServerRuntime(ctx, target.ServerID)
			if err != nil {
				return err
			}
			if !healthyRuntime(runtime, target.TargetVersion, now) {
				commands = withoutBatchCommands(commands, *plan, target.BatchNumber)
				commands = append(commands, c.rollbackBatch(plan, target.BatchNumber, target.ID, "health_check_failed", now)...)
				changed = true
			} else if now.Sub(target.UpdatedAt) >= time.Duration(plan.VerificationSeconds)*time.Second {
				target.Status, target.UpdatedAt, target.FinishedAt = TargetSucceeded, now, &now
				if !target.SourceDraining {
					restoreScheduling = append(restoreScheduling, target.ServerID)
				}
				changed = true
			}
		case TargetRollingBack:
			if now.Sub(target.UpdatedAt) >= time.Duration(plan.ReconnectTimeoutSeconds)*time.Second {
				target.Status, target.ErrorCode, target.ErrorMessage = TargetManualIntervention, "rollback_timeout", "代理回滚后未在时限内重连"
				target.UpdatedAt, target.FinishedAt = now, &now
				plan.Status, plan.PauseReason = PlanPaused, "代理回滚超时，需要人工处理"
				changed = true
			} else {
				commands = append(commands, sentCommand{target.ServerID, rollbackCommand(*plan, *target)})
			}
		}
	}
	if plan.CancelRequested && allTargetsTerminal(*plan) {
		plan.Status, plan.FinishedAt = PlanCancelled, &now
		changed = true
	} else if mayStartTarget && batchSucceeded(*plan, plan.CurrentBatch) {
		if hasBatch(*plan, plan.CurrentBatch+1) {
			plan.CurrentBatch++
			changed = true
		} else {
			plan.Status, plan.FinishedAt = PlanSucceeded, &now
			changed = true
		}
	}
	if changed {
		if _, err = c.store.SavePlan(ctx, *plan); err != nil {
			return err
		}
	}
	for _, command := range commands {
		if err := c.sender.SendUpgradeCommand(ctx, command.serverID, command.command); err != nil {
			return err
		}
	}
	for _, serverID := range restoreScheduling {
		if err := c.store.SetServerDraining(ctx, serverID, false); err != nil {
			return err
		}
	}
	return nil
}

func (c *Coordinator) ApplyUpgradeEvent(ctx context.Context, serverID string, event agentprotocol.UpgradeEvent) error {
	plan, target, index, err := c.matchTarget(ctx, serverID, event.TargetID, event.CommandID)
	if err != nil {
		return err
	}
	now := c.now().UTC()
	occurredAt := event.OccurredAt.UTC()
	if event.OccurredAt.IsZero() {
		occurredAt = now
	}
	persistEvent := func() error {
		return c.store.AppendEvent(ctx, Event{ID: c.newID(), PlanID: plan.ID, TargetID: target.ID, ServerID: serverID, CommandID: event.CommandID, Stage: string(event.Stage), ErrorCode: event.ErrorCode, Message: event.Message, OccurredAt: occurredAt})
	}
	if event.Stage == agentprotocol.StageFailed {
		if event.ErrorCode == "rollback_failed" || target.Status == TargetRollingBack {
			plan.Targets[index].Status, plan.Targets[index].ErrorCode, plan.Targets[index].ErrorMessage = TargetManualIntervention, event.ErrorCode, event.Message
			plan.Targets[index].UpdatedAt = now
			plan.Status, plan.PauseReason = PlanPaused, "代理回滚失败，需要人工处理"
			_ = c.store.SetServerDraining(ctx, serverID, true)
			if _, err = c.store.SavePlan(ctx, plan); err != nil {
				return tolerateConcurrentUpdate(err)
			}
			return persistEvent()
		}
		commands := c.rollbackBatch(&plan, target.BatchNumber, target.ID, event.ErrorCode, now)
		if _, err := c.store.SavePlan(ctx, plan); err != nil {
			return tolerateConcurrentUpdate(err)
		}
		if err := persistEvent(); err != nil {
			return err
		}
		for _, item := range commands {
			if err := c.sender.SendUpgradeCommand(ctx, item.serverID, item.command); err != nil {
				return err
			}
		}
		return nil
	}
	if target.Status == TargetRollingBack && (event.Stage == agentprotocol.StageReconnecting || event.Stage == agentprotocol.StageSucceeded) {
		return persistEvent()
	}
	status, ok := targetStatusForStage(event.Stage)
	if !ok {
		return ErrInvalidTransition
	}
	if target.Status == status {
		return persistEvent()
	}
	if !forwardTransition(target.Status, status) {
		return nil
	}
	plan.Targets[index].Status, plan.Targets[index].UpdatedAt = status, now
	if status == TargetRolledBack {
		plan.Targets[index].FinishedAt = &now
	}
	plan.Targets[index].ErrorCode, plan.Targets[index].ErrorMessage = event.ErrorCode, event.Message
	if _, err = c.store.SavePlan(ctx, plan); err != nil {
		return tolerateConcurrentUpdate(err)
	}
	return persistEvent()
}

func (c *Coordinator) ObserveHeartbeat(ctx context.Context, heartbeat agentprotocol.Heartbeat) error {
	if heartbeat.Upgrade == nil {
		return nil
	}
	plan, target, index, err := c.matchTarget(ctx, heartbeat.ServerID, heartbeat.Upgrade.TargetID, heartbeat.Upgrade.CommandID)
	if err != nil {
		if errors.Is(err, ErrTargetNotFound) {
			return nil
		}
		return err
	}
	now := c.now().UTC()
	if target.Status == TargetRollingBack {
		if heartbeat.AgentVersion != target.SourceVersion {
			return nil
		}
		plan.Targets[index].Status, plan.Targets[index].UpdatedAt, plan.Targets[index].FinishedAt = TargetRolledBack, now, &now
		_, err = c.store.SavePlan(ctx, plan)
		return err
	}
	if target.Status != TargetReconnecting {
		return nil
	}
	if heartbeat.AgentVersion != target.TargetVersion {
		plan.Targets[index].Status, plan.Targets[index].CommandID, plan.Targets[index].UpdatedAt = TargetRollingBack, c.newID(), now
		plan.Status, plan.PauseReason = PlanPaused, "代理重连版本不一致，正在回滚"
		if _, err := c.store.SavePlan(ctx, plan); err != nil {
			return err
		}
		return c.sender.SendUpgradeCommand(ctx, target.ServerID, rollbackCommand(plan, plan.Targets[index]))
	}
	plan.Targets[index].Status, plan.Targets[index].UpdatedAt = TargetHealthChecking, now
	_, err = c.store.SavePlan(ctx, plan)
	return err
}

type sentCommand struct {
	serverID string
	command  agentprotocol.UpgradeCommand
}

func (c *Coordinator) rollbackBatch(plan *Plan, batch int, failedTargetID, errorCode string, now time.Time) []sentCommand {
	plan.Status, plan.PauseReason = PlanPaused, "当前批次升级失败，正在回滚"
	commands := []sentCommand{}
	for index := range plan.Targets {
		target := &plan.Targets[index]
		if target.BatchNumber != batch || target.Status == TargetCancelled || target.Status == TargetRolledBack || target.Status == TargetManualIntervention {
			continue
		}
		uninstalled := target.Status == TargetWaiting || target.Status == TargetDraining || (target.ID == failedTargetID && (target.Status == TargetDownloading || target.Status == TargetVerifying || errorCode == "apply_start_failed" || errorCode == "stage_failed"))
		if uninstalled {
			target.Status, target.UpdatedAt, target.FinishedAt = TargetRolledBack, now, &now
			continue
		}
		target.Status, target.CommandID, target.UpdatedAt, target.FinishedAt = TargetRollingBack, c.newID(), now, nil
		commands = append(commands, sentCommand{target.ServerID, rollbackCommand(*plan, *target)})
	}
	return commands
}

func healthyRuntime(runtime ServerRuntime, targetVersion string, now time.Time) bool {
	return runtime.Enabled && (runtime.Status == "online" || runtime.Status == "draining") && runtime.AgentVersion == targetVersion && runtime.HasSnapshot && runtime.LastSeenAt != nil && now.Sub(runtime.LastSeenAt.UTC()) <= 15*time.Second
}

func withoutBatchCommands(commands []sentCommand, plan Plan, batch int) []sentCommand {
	kept := commands[:0]
	for _, command := range commands {
		remove := false
		for _, target := range plan.Targets {
			if target.BatchNumber == batch && command.command.TargetID == target.ID {
				remove = true
				break
			}
		}
		if !remove {
			kept = append(kept, command)
		}
	}
	return kept
}

func forwardTransition(current, next TargetStatus) bool {
	if current == next {
		return true
	}
	if current == TargetRollingBack && next == TargetRolledBack {
		return true
	}
	rank := map[TargetStatus]int{TargetWaiting: 0, TargetDraining: 1, TargetDownloading: 2, TargetVerifying: 3, TargetInstalling: 4, TargetReconnecting: 5, TargetHealthChecking: 6, TargetSucceeded: 7}
	currentRank, currentOK := rank[current]
	nextRank, nextOK := rank[next]
	return currentOK && nextOK && nextRank > currentRank
}

func tolerateConcurrentUpdate(err error) error {
	if errors.Is(err, ErrInvalidTransition) {
		return nil
	}
	return err
}

func (c *Coordinator) matchTarget(ctx context.Context, serverID, targetID, commandID string) (Plan, Target, int, error) {
	plan, err := c.store.ActivePlan(ctx)
	if err != nil || plan == nil {
		return Plan{}, Target{}, -1, ErrTargetNotFound
	}
	for index, target := range plan.Targets {
		if target.ID == targetID && target.ServerID == serverID && target.CommandID == commandID {
			return *plan, target, index, nil
		}
	}
	return Plan{}, Target{}, -1, ErrTargetNotFound
}

func matchingArtifact(items []ArtifactInfo, osName, arch string) (ArtifactInfo, bool) {
	for _, item := range items {
		if (osName == "" || item.OS == osName) && (arch == "" || item.Arch == arch) {
			return item, true
		}
	}
	return ArtifactInfo{}, false
}
func installCommand(plan Plan, target Target, a ArtifactInfo) agentprotocol.UpgradeCommand {
	return agentprotocol.UpgradeCommand{CommandID: target.CommandID, PlanID: plan.ID, TargetID: target.ID, Action: agentprotocol.UpgradeInstall, SourceVersion: target.SourceVersion, TargetVersion: target.TargetVersion, DownloadURL: a.DownloadURL, FileName: a.FileName, ByteSize: a.ByteSize, SHA256: a.SHA256, ReconnectTimeout: time.Duration(plan.ReconnectTimeoutSeconds) * time.Second}
}
func rollbackCommand(plan Plan, target Target) agentprotocol.UpgradeCommand {
	return agentprotocol.UpgradeCommand{CommandID: target.CommandID, PlanID: plan.ID, TargetID: target.ID, Action: agentprotocol.UpgradeRollback, SourceVersion: target.TargetVersion, TargetVersion: target.SourceVersion, InstallCommandID: target.InstallCommandID, ReconnectTimeout: time.Duration(plan.ReconnectTimeoutSeconds) * time.Second}
}
func targetStatusForStage(stage agentprotocol.UpgradeStage) (TargetStatus, bool) {
	m := map[agentprotocol.UpgradeStage]TargetStatus{agentprotocol.StageAccepted: TargetDownloading, agentprotocol.StageDownloading: TargetDownloading, agentprotocol.StageVerifying: TargetVerifying, agentprotocol.StageInstalling: TargetInstalling, agentprotocol.StageReconnecting: TargetReconnecting, agentprotocol.StageSucceeded: TargetReconnecting, agentprotocol.StageRollingBack: TargetRollingBack, agentprotocol.StageRolledBack: TargetRolledBack}
	v, ok := m[stage]
	return v, ok
}
func batchSucceeded(plan Plan, batch int) bool {
	found := false
	for _, t := range plan.Targets {
		if t.BatchNumber == batch {
			found = true
			if t.Status != TargetSucceeded {
				return false
			}
		}
	}
	return found
}
func hasBatch(plan Plan, batch int) bool {
	for _, t := range plan.Targets {
		if t.BatchNumber == batch {
			return true
		}
	}
	return false
}

func allTargetsTerminal(plan Plan) bool {
	for _, target := range plan.Targets {
		if !terminalTargetStatus(target.Status) {
			return false
		}
	}
	return true
}
