package agentupgrade

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"yunling.local/platform/internal/auth"
)

func TestResumeBindsUnstartedTargetsToTheirSourceVersion(t *testing.T) {
	for _, status := range []TargetStatus{TargetWaiting, TargetDraining} {
		for _, test := range []struct {
			name, current string
			missing       bool
			want          error
		}{
			{name: "original source", current: "0.2.4"},
			{name: "newer version installed manually", current: "0.2.10", want: ErrUpgradeSourceChanged},
			{name: "target already installed", current: "0.2.6", want: ErrUpgradeSourceChanged},
			{name: "different source identifier", current: "v0.2.4", want: ErrUpgradeSourceChanged},
			{name: "missing server", missing: true, want: ErrServerIneligible},
		} {
			t.Run(string(status)+"/"+test.name, func(t *testing.T) {
				repository, service, before := pausedExecutionPlan(t, status)
				if test.missing {
					delete(repository.servers, "s1")
				} else {
					server := repository.servers["s1"]
					server.AgentVersion = test.current
					repository.servers[server.ID] = server
				}
				plan, err := service.Resume(context.Background(), before.ID)
				if !errors.Is(err, test.want) {
					t.Fatalf("Resume error=%v, want=%v", err, test.want)
				}
				if test.want != nil {
					assertExecutionPlanUnchanged(t, repository, before)
					return
				}
				if repository.saves != 1 || plan.Status != PlanRunning || plan.PauseReason != "" ||
					!reflect.DeepEqual(plan.Targets, before.Targets) {
					t.Fatalf("original source should resume without rewriting targets: %+v, saves=%d", plan, repository.saves)
				}
			})
		}
	}
}

func TestResumeAllowsInFlightTargetsToReportInstalledVersion(t *testing.T) {
	for _, status := range []TargetStatus{TargetDownloading, TargetVerifying, TargetInstalling, TargetReconnecting, TargetHealthChecking} {
		t.Run(string(status), func(t *testing.T) {
			repository, service, before := pausedExecutionPlan(t, status)
			server := repository.servers["s1"]
			server.AgentVersion = "0.2.6"
			repository.servers[server.ID] = server
			plan, err := service.Resume(context.Background(), before.ID)
			if err != nil || repository.saves != 1 || plan.Status != PlanRunning || !reflect.DeepEqual(plan.Targets, before.Targets) {
				t.Fatalf("in-flight target should resume at reported target version: plan=%+v err=%v saves=%d", plan, err, repository.saves)
			}
		})
	}
}

func TestRetryBindsResetTargetsToTheirSourceVersion(t *testing.T) {
	for _, status := range []TargetStatus{TargetRolledBack, TargetManualIntervention} {
		for _, test := range []struct {
			name, current string
			missing       bool
			want          error
		}{
			{name: "original source", current: "0.2.4"},
			{name: "newer version installed manually", current: "0.2.10", want: ErrUpgradeSourceChanged},
			{name: "target already installed", current: "0.2.6", want: ErrUpgradeSourceChanged},
			{name: "different source identifier", current: "v0.2.4", want: ErrUpgradeSourceChanged},
			{name: "missing server", missing: true, want: ErrServerIneligible},
		} {
			t.Run(string(status)+"/"+test.name, func(t *testing.T) {
				repository, service, before := pausedExecutionPlan(t, status)
				if test.missing {
					delete(repository.servers, "s1")
				} else {
					server := repository.servers["s1"]
					server.AgentVersion = test.current
					repository.servers[server.ID] = server
				}
				plan, err := service.RetryTarget(context.Background(), before.ID, before.Targets[0].ID)
				if !errors.Is(err, test.want) {
					t.Fatalf("RetryTarget error=%v, want=%v", err, test.want)
				}
				if test.want != nil {
					assertExecutionPlanUnchanged(t, repository, before)
					return
				}
				target := plan.Targets[0]
				if repository.saves != 1 || plan.Status != PlanRunning || target.Status != TargetDraining ||
					target.CommandID == before.Targets[0].CommandID || target.InstallCommandID != target.CommandID ||
					target.Attempts != before.Targets[0].Attempts+1 || target.SourceVersion != "0.2.4" || target.TargetVersion != "0.2.6" ||
					target.ErrorCode != "" || target.ErrorMessage != "" || target.FinishedAt != nil {
					t.Fatalf("original source should reset the same transition for retry: %+v, saves=%d", plan, repository.saves)
				}
			})
		}
	}
}

func TestExecutionSourceChecksPreserveIdempotentControls(t *testing.T) {
	for _, status := range []PlanStatus{PlanRunning, PlanPending} {
		t.Run("resume/"+string(status), func(t *testing.T) {
			repository, service, before := pausedExecutionPlan(t, TargetWaiting)
			before.Status = status
			repository.plans[before.ID] = before
			delete(repository.servers, "s1")
			plan, err := service.Resume(context.Background(), before.ID)
			if err != nil || !reflect.DeepEqual(plan, before) || repository.serverLookups != 0 {
				t.Fatalf("idempotent resume changed behavior: plan=%+v err=%v lookups=%d", plan, err, repository.serverLookups)
			}
			assertExecutionPlanUnchanged(t, repository, before)
		})
	}
	for _, status := range []TargetStatus{TargetDraining, TargetDownloading, TargetVerifying, TargetInstalling, TargetReconnecting, TargetHealthChecking} {
		t.Run("retry/"+string(status), func(t *testing.T) {
			repository, service, before := pausedExecutionPlan(t, status)
			delete(repository.servers, "s1")
			plan, err := service.RetryTarget(context.Background(), before.ID, before.Targets[0].ID)
			if err != nil || !reflect.DeepEqual(plan, before) || repository.serverLookups != 0 {
				t.Fatalf("idempotent retry changed behavior: plan=%+v err=%v lookups=%d", plan, err, repository.serverLookups)
			}
			assertExecutionPlanUnchanged(t, repository, before)
		})
	}
}

func TestExecutionSourceChangedHTTPReturnsConflict(t *testing.T) {
	for _, action := range []string{"resume", "retry"} {
		t.Run(action, func(t *testing.T) {
			status := TargetWaiting
			if action == "retry" {
				status = TargetRolledBack
			}
			repository, service, before := pausedExecutionPlan(t, status)
			server := repository.servers["s1"]
			server.AgentVersion = "0.2.10"
			repository.servers[server.ID] = server
			path := "/api/agent-upgrades/" + before.ID + "/resume"
			if action == "retry" {
				path = "/api/agent-upgrades/" + before.ID + "/targets/" + before.Targets[0].ID + "/retry"
			}
			recorder := httptest.NewRecorder()
			ManagementHandler(service).ServeHTTP(recorder, upgradeRequest(http.MethodPost, path, "", auth.RoleAdmin))
			if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), ErrUpgradeSourceChanged.Error()) {
				t.Fatalf("source conflict HTTP response: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			assertExecutionPlanUnchanged(t, repository, before)
		})
	}
}

func pausedExecutionPlan(t *testing.T, status TargetStatus) (*executionServiceRepository, *Service, Plan) {
	t.Helper()
	repository := &executionServiceRepository{memoryRepository: validMemoryRepository()}
	server := repository.servers["s1"]
	server.AgentVersion = "0.2.4"
	repository.servers[server.ID] = server
	release := repository.releases["release-2"]
	release.Version = "0.2.6"
	repository.releases[release.ID] = release
	service := NewService(repository)
	plan, err := service.CreatePlan(context.Background(), CreatePlanInput{TargetReleaseID: release.ID, ServerIDs: []string{server.ID}, CreatedBy: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if plan, err = service.Pause(context.Background(), plan.ID, "manual pause"); err != nil {
		t.Fatal(err)
	}
	plan.Targets[0].Status = status
	plan.Targets[0].Attempts = 1
	plan.Targets[0].ErrorCode, plan.Targets[0].ErrorMessage = "previous_failure", "previous attempt failed"
	repository.plans[plan.ID] = plan
	repository.saves, repository.serverLookups = 0, 0
	plan.Targets = append([]Target(nil), plan.Targets...)
	return repository, service, plan
}

func assertExecutionPlanUnchanged(t *testing.T, repository *executionServiceRepository, before Plan) {
	t.Helper()
	if repository.saves != 0 || !reflect.DeepEqual(repository.plans[before.ID], before) {
		t.Fatalf("rejected or idempotent control modified the plan: saves=%d before=%+v after=%+v", repository.saves, before, repository.plans[before.ID])
	}
}

type executionServiceRepository struct {
	*memoryRepository
	saves, serverLookups int
}

func (r *executionServiceRepository) Servers(_ context.Context, ids []string) ([]ServerInfo, error) {
	r.serverLookups++
	servers := make([]ServerInfo, 0, len(ids))
	for _, id := range ids {
		if server, exists := r.servers[id]; exists {
			servers = append(servers, server)
		}
	}
	return servers, nil
}

func (r *executionServiceRepository) SavePlan(ctx context.Context, plan Plan) (Plan, error) {
	r.saves++
	return r.memoryRepository.SavePlan(ctx, plan)
}
