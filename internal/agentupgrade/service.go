package agentupgrade

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const selfUpgradeCapability = "self_upgrade_v1"

type Repository interface {
	ActivePlan(context.Context) (*Plan, error)
	Release(context.Context, string) (ReleaseInfo, error)
	ReleaseByVersion(context.Context, string) (ReleaseInfo, error)
	Servers(context.Context, []string) ([]ServerInfo, error)
	CreatePlan(context.Context, Plan) (Plan, error)
	ListPlans(context.Context) ([]Plan, error)
	Plan(context.Context, string) (Plan, error)
	SavePlan(context.Context, Plan) (Plan, error)
}

type Service struct {
	repository Repository
	now        func() time.Time
	newID      func() string
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository, now: time.Now, newID: uuid.NewString}
}

func (s *Service) CreatePlan(ctx context.Context, input CreatePlanInput) (Plan, error) {
	applyPlanDefaults(&input)
	if err := validatePlanInput(input); err != nil {
		return Plan{}, err
	}
	active, err := s.repository.ActivePlan(ctx)
	if err != nil {
		return Plan{}, err
	}
	if active != nil {
		return Plan{}, ErrActivePlanExists
	}
	release, err := s.repository.Release(ctx, input.TargetReleaseID)
	if err != nil {
		return Plan{}, err
	}
	if release.Status != "available" || !slices.Contains(release.Capabilities, selfUpgradeCapability) {
		return Plan{}, ErrUpgradeUnsupported
	}
	servers, err := s.repository.Servers(ctx, input.ServerIDs)
	if err != nil {
		return Plan{}, err
	}
	if len(servers) != len(input.ServerIDs) {
		return Plan{}, ErrServerIneligible
	}
	now := s.now().UTC()
	plan := Plan{
		ID: s.newID(), TargetReleaseID: release.ID, TargetVersion: release.Version,
		Status: PlanRunning, FirstBatchSize: 1, BatchSize: input.BatchSize,
		DrainTimeoutSeconds: input.DrainTimeoutSeconds, ReconnectTimeoutSeconds: input.ReconnectTimeoutSeconds,
		VerificationSeconds: input.VerificationSeconds, CurrentBatch: 1,
		CreatedBy: input.CreatedBy, CreatedAt: now, StartedAt: &now, Targets: make([]Target, 0, len(servers)),
	}
	for index, server := range servers {
		if !server.Enabled || (server.Status != "online" && server.Status != "draining") {
			return Plan{}, ErrServerIneligible
		}
		if !slices.Contains(server.Capabilities, selfUpgradeCapability) {
			return Plan{}, ErrUpgradeUnsupported
		}
		if server.AgentVersion == release.Version {
			return Plan{}, ErrNoUpgradeNeeded
		}
		if !hasArtifact(release, server.AgentOS, server.AgentArch) {
			return Plan{}, ErrArtifactUnavailable
		}
		batch := 1
		if index > 0 {
			batch = 2 + (index-1)/input.BatchSize
		}
		commandID := s.newID()
		plan.Targets = append(plan.Targets, Target{
			ID: s.newID(), PlanID: plan.ID, ServerID: server.ID, BatchNumber: batch,
			SourceVersion: server.AgentVersion, TargetVersion: release.Version, SourceDraining: server.Draining,
			Status: TargetWaiting, CommandID: commandID, InstallCommandID: commandID, UpdatedAt: now,
		})
	}
	created, err := s.repository.CreatePlan(ctx, plan)
	if errors.Is(err, ErrActivePlanExists) {
		return Plan{}, ErrActivePlanExists
	}
	return created, err
}

func (s *Service) ListPlans(ctx context.Context) ([]Plan, error) { return s.repository.ListPlans(ctx) }
func (s *Service) Plan(ctx context.Context, id string) (Plan, error) {
	return s.repository.Plan(ctx, id)
}

func (s *Service) Pause(ctx context.Context, id, reason string) (Plan, error) {
	plan, err := s.repository.Plan(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if plan.Status == PlanPaused {
		return plan, nil
	}
	if plan.Status != PlanPending && plan.Status != PlanRunning {
		return Plan{}, ErrInvalidTransition
	}
	plan.Status, plan.PauseReason = PlanPaused, strings.TrimSpace(reason)
	return s.repository.SavePlan(ctx, plan)
}

func (s *Service) Resume(ctx context.Context, id string) (Plan, error) {
	plan, err := s.repository.Plan(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if plan.Status == PlanRunning || plan.Status == PlanPending {
		return plan, nil
	}
	if plan.Status != PlanPaused {
		return Plan{}, ErrInvalidTransition
	}
	if plan.CancelRequested {
		return Plan{}, ErrInvalidTransition
	}
	release, err := s.repository.Release(ctx, plan.TargetReleaseID)
	if err != nil {
		return Plan{}, err
	}
	if release.Status != "available" || !slices.Contains(release.Capabilities, selfUpgradeCapability) {
		return Plan{}, ErrUpgradeUnsupported
	}
	serverIDs := make([]string, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		if target.Status == TargetWaiting || target.Status == TargetDraining {
			serverIDs = append(serverIDs, target.ServerID)
		}
	}
	servers, err := s.repository.Servers(ctx, serverIDs)
	if err != nil {
		return Plan{}, err
	}
	for _, server := range servers {
		if !server.Enabled || (server.Status != "online" && server.Status != "draining") {
			return Plan{}, ErrServerIneligible
		}
		if !slices.Contains(server.Capabilities, selfUpgradeCapability) {
			return Plan{}, ErrUpgradeUnsupported
		}
		if !hasArtifact(release, server.AgentOS, server.AgentArch) {
			return Plan{}, ErrArtifactUnavailable
		}
	}
	plan.Status, plan.PauseReason = PlanRunning, ""
	return s.repository.SavePlan(ctx, plan)
}

func (s *Service) Cancel(ctx context.Context, id string) (Plan, error) {
	plan, err := s.repository.Plan(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if plan.Status == PlanCancelled || plan.CancelRequested {
		return plan, nil
	}
	if plan.Status != PlanPending && plan.Status != PlanRunning && plan.Status != PlanPaused {
		return Plan{}, ErrInvalidTransition
	}
	now := s.now().UTC()
	started := false
	for index := range plan.Targets {
		if plan.Targets[index].Status == TargetWaiting {
			plan.Targets[index].Status = TargetCancelled
			plan.Targets[index].UpdatedAt, plan.Targets[index].FinishedAt = now, &now
		} else if !terminalTargetStatus(plan.Targets[index].Status) {
			started = true
		}
	}
	if started {
		plan.Status, plan.CancelRequested, plan.PauseReason = PlanPaused, true, "计划已取消，正在完成已开始节点"
	} else {
		plan.Status, plan.CancelRequested, plan.FinishedAt = PlanCancelled, true, &now
	}
	return s.repository.SavePlan(ctx, plan)
}

func (s *Service) RetryTarget(ctx context.Context, planID, targetID string) (Plan, error) {
	plan, err := s.repository.Plan(ctx, planID)
	if err != nil {
		return Plan{}, err
	}
	if plan.CancelRequested {
		return Plan{}, ErrInvalidTransition
	}
	for index := range plan.Targets {
		target := &plan.Targets[index]
		if target.ID != targetID {
			continue
		}
		if target.Status == TargetDraining || target.Status == TargetDownloading || target.Status == TargetVerifying || target.Status == TargetInstalling || target.Status == TargetReconnecting || target.Status == TargetHealthChecking {
			return plan, nil
		}
		if target.Status != TargetRolledBack && target.Status != TargetManualIntervention {
			return Plan{}, ErrInvalidTransition
		}
		target.Status, target.CommandID = TargetDraining, s.newID()
		target.InstallCommandID = target.CommandID
		target.Attempts++
		target.ErrorCode, target.ErrorMessage, target.FinishedAt = "", "", nil
		target.UpdatedAt = s.now().UTC()
		plan.Status, plan.PauseReason = PlanRunning, ""
		return s.repository.SavePlan(ctx, plan)
	}
	return Plan{}, ErrTargetNotFound
}

func (s *Service) CreateRollbackPlan(ctx context.Context, planID, targetID, actorID string) (Plan, error) {
	original, err := s.repository.Plan(ctx, planID)
	if err != nil {
		return Plan{}, err
	}
	var source *Target
	for index := range original.Targets {
		if original.Targets[index].ID == targetID {
			source = &original.Targets[index]
			break
		}
	}
	if source == nil {
		return Plan{}, ErrTargetNotFound
	}
	if source.Status == TargetRollingBack || source.Status == TargetRolledBack {
		return original, nil
	}
	if source.Status != TargetSucceeded {
		return Plan{}, ErrInvalidTransition
	}
	release, err := s.repository.ReleaseByVersion(ctx, source.SourceVersion)
	if err != nil {
		return Plan{}, err
	}
	active, err := s.repository.ActivePlan(ctx)
	if err != nil {
		return Plan{}, err
	}
	if active != nil {
		return Plan{}, ErrActivePlanExists
	}
	if original.Status == PlanSucceeded || original.Status == PlanCancelled {
		return s.CreatePlan(ctx, CreatePlanInput{
			TargetReleaseID: release.ID, ServerIDs: []string{source.ServerID}, BatchSize: 1,
			DrainTimeoutSeconds: original.DrainTimeoutSeconds, ReconnectTimeoutSeconds: original.ReconnectTimeoutSeconds,
			VerificationSeconds: original.VerificationSeconds, CreatedBy: actorID,
		})
	}
	return Plan{}, ErrInvalidTransition
}

func applyPlanDefaults(input *CreatePlanInput) {
	if input.BatchSize == 0 {
		input.BatchSize = 1
	}
	if input.DrainTimeoutSeconds == 0 {
		input.DrainTimeoutSeconds = 3600
	}
	if input.ReconnectTimeoutSeconds == 0 {
		input.ReconnectTimeoutSeconds = 120
	}
	if input.VerificationSeconds == 0 {
		input.VerificationSeconds = 30
	}
}

func validatePlanInput(input CreatePlanInput) error {
	if strings.TrimSpace(input.TargetReleaseID) == "" || strings.TrimSpace(input.CreatedBy) == "" || len(input.ServerIDs) == 0 || input.BatchSize < 1 || input.BatchSize > 100 || input.DrainTimeoutSeconds < 60 || input.DrainTimeoutSeconds > 86400 || input.ReconnectTimeoutSeconds < 30 || input.ReconnectTimeoutSeconds > 3600 || input.VerificationSeconds < 10 || input.VerificationSeconds > 600 {
		return ErrInvalidPlan
	}
	seen := map[string]bool{}
	for _, id := range input.ServerIDs {
		if strings.TrimSpace(id) == "" || seen[id] {
			return ErrInvalidPlan
		}
		seen[id] = true
	}
	return nil
}
func hasArtifact(release ReleaseInfo, osName, arch string) bool {
	for _, artifact := range release.Artifacts {
		if artifact.OS == osName && artifact.Arch == arch {
			return true
		}
	}
	return false
}

func terminalTargetStatus(status TargetStatus) bool {
	return status == TargetSucceeded || status == TargetRolledBack || status == TargetManualIntervention || status == TargetCancelled
}
