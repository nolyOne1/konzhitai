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
