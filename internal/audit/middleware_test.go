package audit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"yunling.local/platform/internal/audit"
	"yunling.local/platform/internal/auth"
)

func TestMiddlewareRecordsSuccessfulCriticalMutation(t *testing.T) {
	repository := &memoryAuditRepository{}
	service := audit.NewService(repository, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := audit.Middleware(service)(next)
	request := httptest.NewRequest(http.MethodPost, "/api/runs/run-1/cancel", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "operator-1"}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if len(repository.events) != 1 || repository.events[0].Action != "run.cancel" || repository.events[0].TargetID != "run-1" || repository.events[0].ActorID != "operator-1" {
		t.Fatalf("成功终止任务必须记录操作者和目标：%+v", repository.events)
	}
}

func TestMiddlewareDoesNotRecordFailedOrReadOnlyRequest(t *testing.T) {
	repository := &memoryAuditRepository{}
	service := audit.NewService(repository, nil)
	failed := audit.Middleware(service)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "失败", http.StatusBadRequest) }))
	failed.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/tasks/task-1/run", nil))
	read := audit.Middleware(service)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	read.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/runs", nil))
	if len(repository.events) != 0 {
		t.Fatalf("失败请求和只读请求不应写成功审计：%+v", repository.events)
	}
}

func TestMiddlewareClassifiesAgentReleaseAndUpgradeMutations(t *testing.T) {
	for _, test := range []struct{ path, action string }{
		{"/api/agent-releases/release-1/recommend", "agent_release.recommend"},
		{"/api/agent-releases/release-1/withdraw", "agent_release.withdraw"},
		{"/api/agent-upgrades", "agent_upgrade.create"},
		{"/api/agent-upgrades/plan-1/pause", "agent_upgrade.pause"},
		{"/api/agent-upgrades/plan-1/resume", "agent_upgrade.resume"},
		{"/api/agent-upgrades/plan-1/cancel", "agent_upgrade.cancel"},
		{"/api/agent-upgrades/plan-1/targets/target-1/retry", "agent_upgrade.retry"},
		{"/api/agent-upgrades/plan-1/targets/target-1/rollback", "agent_upgrade.rollback"},
	} {
		t.Run(test.action, func(t *testing.T) {
			repository := &memoryAuditRepository{}
			handler := audit.Middleware(audit.NewService(repository, nil))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "admin-1"}))
			handler.ServeHTTP(httptest.NewRecorder(), request)
			if len(repository.events) != 1 || repository.events[0].Action != test.action {
				t.Fatalf("审计动作错误：%+v", repository.events)
			}
		})
	}
}
