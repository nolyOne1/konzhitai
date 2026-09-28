package agentupgrade

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yunling.local/platform/internal/auth"
)

func TestCreatePlanRequiresNewerStableVersion(t *testing.T) {
	for _, test := range []struct {
		name, current, target string
		want                  error
	}{
		{"downgrade", "0.2.6", "0.2.0", ErrDowngradeNotAllowed},
		{"numeric patch order", "0.2.6", "0.2.10", nil},
		{"prefixed higher target", "0.2.6", "v0.2.10", nil},
		{"same version", "0.2.6", "0.2.6", ErrNoUpgradeNeeded},
		{"equivalent current prefix", "v0.2.6", "0.2.6", ErrNoUpgradeNeeded},
		{"equivalent target prefix", "0.2.6", "v0.2.6", ErrNoUpgradeNeeded},
		{"unknown current", "dev", "0.2.10", ErrVersionNotComparable},
		{"unknown target", "0.2.6", "custom-build", ErrVersionNotComparable},
		{"prerelease current", "0.2.6-rc.1", "0.2.10", ErrVersionNotComparable},
		{"prerelease target", "0.2.6", "0.2.10-rc.1", ErrVersionNotComparable},
		{"build metadata target", "0.2.6", "0.2.10+build.1", ErrVersionNotComparable},
		{"leading zero target", "0.2.6", "0.2.010", ErrVersionNotComparable},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &planCreationRecorder{memoryRepository: validMemoryRepository()}
			server := repository.servers["s1"]
			server.AgentVersion = test.current
			repository.servers["s1"] = server
			release := repository.releases["release-2"]
			release.Version = test.target
			repository.releases["release-2"] = release
			plan, err := NewService(repository).CreatePlan(context.Background(), CreatePlanInput{
				TargetReleaseID: release.ID, ServerIDs: []string{server.ID}, CreatedBy: "user-1",
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want=%v", err, test.want)
			}
			if test.want != nil {
				if plan.ID != "" || repository.creates != 0 || len(repository.plans) != 0 || repository.active != nil {
					t.Fatalf("rejected upgrade wrote a plan: plan=%+v calls=%d stored=%d", plan, repository.creates, len(repository.plans))
				}
				return
			}
			if repository.creates != 1 || len(plan.Targets) != 1 || plan.Status != PlanRunning ||
				plan.Targets[0].SourceVersion != test.current || plan.Targets[0].TargetVersion != test.target {
				t.Fatalf("newer version was not preserved in its upgrade plan: %+v", plan)
			}
		})
	}
}

func TestCreatePlanRejectsMixedDirectionBeforeWritingAnyTarget(t *testing.T) {
	repository := &planCreationRecorder{memoryRepository: validMemoryRepository()}
	release := repository.releases["release-2"]
	release.Version = "0.2.4"
	repository.releases[release.ID] = release
	server := repository.servers["s2"]
	server.AgentVersion = "0.2.6"
	repository.servers[server.ID] = server
	_, err := NewService(repository).CreatePlan(context.Background(), CreatePlanInput{
		TargetReleaseID: release.ID, ServerIDs: []string{"s1", "s2"}, CreatedBy: "user-1",
	})
	if !errors.Is(err, ErrDowngradeNotAllowed) || repository.creates != 0 || len(repository.plans) != 0 {
		t.Fatalf("mixed upgrade/downgrade must not create a partial plan: err=%v calls=%d", err, repository.creates)
	}
}

func TestCreateRollbackPlanRequiresCurrentHistoricalTargetVersion(t *testing.T) {
	for _, test := range []struct {
		name    string
		current string
		modify  func(*memoryRepository)
		want    error
	}{
		{name: "explicit historical rollback", current: "0.2.6"},
		{name: "later version installed", current: "0.2.10", want: ErrRollbackSourceChanged},
		{name: "already at original source", current: "0.2.4", want: ErrRollbackSourceChanged},
		{name: "different version identifier", current: "v0.2.6", want: ErrRollbackSourceChanged},
		{name: "unknown current version", current: "dev", want: ErrRollbackSourceChanged},
		{name: "historical target not successful", current: "0.2.6", modify: func(r *memoryRepository) {
			plan := r.plans["historical-plan"]
			plan.Targets[0].Status = TargetWaiting
			r.plans[plan.ID] = plan
		}, want: ErrInvalidTransition},
		{name: "original release withdrawn", current: "0.2.6", modify: func(r *memoryRepository) {
			release := r.releases["release-1"]
			release.Status = "withdrawn"
			r.releases[release.ID] = release
		}, want: ErrUpgradeUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &planCreationRecorder{memoryRepository: repositoryWithCompletedUpgrade(test.current)}
			if test.modify != nil {
				test.modify(repository.memoryRepository)
			}
			plan, err := NewService(repository).CreateRollbackPlan(context.Background(), "historical-plan", "historical-target", "user-1")
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want=%v", err, test.want)
			}
			if test.want != nil {
				if repository.creates != 0 || len(repository.plans) != 1 || repository.active != nil {
					t.Fatalf("invalid historical rollback wrote a new plan: calls=%d plans=%d", repository.creates, len(repository.plans))
				}
				return
			}
			if repository.creates != 1 || plan.ID == "historical-plan" || plan.Status != PlanRunning ||
				plan.TargetReleaseID != "release-1" || plan.TargetVersion != "0.2.4" || len(plan.Targets) != 1 ||
				plan.Targets[0].ServerID != "s1" || plan.Targets[0].SourceVersion != "0.2.6" || plan.Targets[0].TargetVersion != "0.2.4" {
				t.Fatalf("explicit rollback must create the bound historical transition: %+v", plan)
			}
		})
	}
}

func TestUpgradeHTTPRejectsDirectionAndIgnoresDowngradeBypassInput(t *testing.T) {
	for _, test := range []struct {
		name, current, wantMessage string
	}{
		{"lower target", "0.2.6", ErrDowngradeNotAllowed.Error()},
		{"unknown current", "dev", ErrVersionNotComparable.Error()},
		{"equivalent prefix", "v0.2.0", ErrNoUpgradeNeeded.Error()},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &planCreationRecorder{memoryRepository: validMemoryRepository()}
			server := repository.servers["s1"]
			server.AgentVersion = test.current
			repository.servers[server.ID] = server
			recorder := httptest.NewRecorder()
			ManagementHandler(NewService(repository)).ServeHTTP(recorder, upgradeRequest(http.MethodPost, "/api/agent-upgrades",
				`{"target_release_id":"release-2","server_ids":["s1"],"allowDowngrade":true,"allow_downgrade":true}`, auth.RoleAdmin))
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), test.wantMessage) || repository.creates != 0 {
				t.Fatalf("normal HTTP input cannot enable downgrades: status=%d body=%s calls=%d", recorder.Code, recorder.Body.String(), repository.creates)
			}
		})
	}
}

func TestRollbackHTTPRejectsStaleHistoryAsConflict(t *testing.T) {
	repository := &planCreationRecorder{memoryRepository: repositoryWithCompletedUpgrade("0.2.10")}
	recorder := httptest.NewRecorder()
	ManagementHandler(NewService(repository)).ServeHTTP(recorder, upgradeRequest(http.MethodPost,
		"/api/agent-upgrades/historical-plan/targets/historical-target/rollback", "", auth.RoleAdmin))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), ErrRollbackSourceChanged.Error()) || repository.creates != 0 {
		t.Fatalf("stale rollback response: status=%d body=%s calls=%d", recorder.Code, recorder.Body.String(), repository.creates)
	}
}

type planCreationRecorder struct {
	*memoryRepository
	creates int
}

func (r *planCreationRecorder) CreatePlan(ctx context.Context, plan Plan) (Plan, error) {
	r.creates++
	return r.memoryRepository.CreatePlan(ctx, plan)
}

func repositoryWithCompletedUpgrade(currentVersion string) *memoryRepository {
	repository := validMemoryRepository()
	previous := repository.releases["release-1"]
	previous.Version = "0.2.4"
	repository.releases[previous.ID] = previous
	server := repository.servers["s1"]
	server.AgentVersion = currentVersion
	repository.servers[server.ID] = server
	repository.plans["historical-plan"] = Plan{
		ID: "historical-plan", TargetReleaseID: "release-2", TargetVersion: "0.2.6", Status: PlanSucceeded,
		Targets: []Target{{ID: "historical-target", PlanID: "historical-plan", ServerID: "s1",
			SourceVersion: "0.2.4", TargetVersion: "0.2.6", Status: TargetSucceeded}},
	}
	return repository
}
