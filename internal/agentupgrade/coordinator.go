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
			if err := c.store.SetServerDraining(ctx, target.ServerID, true); err != nil {
				return err
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
			command := installCommand(*plan, *target, artifact)
			if err := c.sender.SendUpgradeCommand(ctx, target.ServerID, command); err != nil {
				return err
			}
			target.Status, target.Attempts, target.UpdatedAt = TargetDownloading, target.Attempts+1, now
			changed = true
		case TargetHealthChecking:
			if now.Sub(target.UpdatedAt) >= time.Duration(plan.VerificationSeconds)*time.Second {
				target.Status, target.UpdatedAt, target.FinishedAt = TargetSucceeded, now, &now
				if !target.SourceDraining {
					if err := c.store.SetServerDraining(ctx, target.ServerID, false); err != nil {
						return err
					}
				}
				changed = true
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
		_, err = c.store.SavePlan(ctx, *plan)
	}
	return err
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
	if err := c.store.AppendEvent(ctx, Event{ID: c.newID(), PlanID: plan.ID, TargetID: target.ID, ServerID: serverID, CommandID: event.CommandID, Stage: string(event.Stage), ErrorCode: event.ErrorCode, Message: event.Message, OccurredAt: occurredAt}); err != nil {
		return err
	}
	if event.Stage == agentprotocol.StageFailed {
		if event.ErrorCode == "rollback_failed" || target.Status == TargetRollingBack {
			plan.Targets[index].Status, plan.Targets[index].ErrorCode, plan.Targets[index].ErrorMessage = TargetManualIntervention, event.ErrorCode, event.Message
			plan.Targets[index].UpdatedAt = now
			plan.Status, plan.PauseReason = PlanPaused, "代理回滚失败，需要人工处理"
			_ = c.store.SetServerDraining(ctx, serverID, true)
			_, err = c.store.SavePlan(ctx, plan)
			return err
		}
		plan.Status, plan.PauseReason = PlanPaused, "当前批次升级失败，正在回滚"
		commands := []struct {
			server  string
			command agentprotocol.UpgradeCommand
		}{}
		for i := range plan.Targets {
			item := &plan.Targets[i]
			if item.BatchNumber != target.BatchNumber || item.Status == TargetSucceeded || item.Status == TargetWaiting || item.Status == TargetCancelled {
				continue
			}
			item.Status, item.CommandID, item.UpdatedAt = TargetRollingBack, c.newID(), now
			commands = append(commands, struct {
				server  string
				command agentprotocol.UpgradeCommand
			}{item.ServerID, rollbackCommand(plan, *item)})
		}
		if _, err := c.store.SavePlan(ctx, plan); err != nil {
			return err
		}
		for _, item := range commands {
			if err := c.sender.SendUpgradeCommand(ctx, item.server, item.command); err != nil {
				return err
			}
		}
		return nil
	}
	if target.Status == TargetRollingBack && (event.Stage == agentprotocol.StageReconnecting || event.Stage == agentprotocol.StageSucceeded) {
		plan.Targets[index].UpdatedAt = now
		_, err = c.store.SavePlan(ctx, plan)
		return err
	}
	status, ok := targetStatusForStage(event.Stage)
	if !ok {
		return ErrInvalidTransition
	}
	plan.Targets[index].Status, plan.Targets[index].UpdatedAt = status, now
	if status == TargetRolledBack {
		plan.Targets[index].FinishedAt = &now
	}
	plan.Targets[index].ErrorCode, plan.Targets[index].ErrorMessage = event.ErrorCode, event.Message
	_, err = c.store.SavePlan(ctx, plan)
	return err
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
		if _, err := c.store.SavePlan(ctx, plan); err != nil {
			return err
		}
		return c.sender.SendUpgradeCommand(ctx, target.ServerID, rollbackCommand(plan, plan.Targets[index]))
	}
	plan.Targets[index].Status, plan.Targets[index].UpdatedAt = TargetHealthChecking, now
	_, err = c.store.SavePlan(ctx, plan)
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
	return agentprotocol.UpgradeCommand{CommandID: target.CommandID, PlanID: plan.ID, TargetID: target.ID, Action: agentprotocol.UpgradeRollback, SourceVersion: target.TargetVersion, TargetVersion: target.SourceVersion, ReconnectTimeout: time.Duration(plan.ReconnectTimeoutSeconds) * time.Second}
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
