package task

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
)

func TestRunHandlerListsRunsAndStreamsChineseEvents(t *testing.T) {
	manager := &fakeRunManager{
		runs:   []RunView{{ID: "run-1", TaskName: "每日归档", State: Running}},
		events: []RunStreamEvent{{ID: "event-1", Kind: "state", State: Running, Message: "任务已开始执行", OccurredAt: time.Now()}},
	}
	handler := RunHandler(manager)
	request := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "viewer", Roles: []auth.RoleName{auth.RoleViewer}}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "每日归档") {
		t.Fatalf("运行列表响应不正确：%d %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/runs/run-1/events?follow=false", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "viewer", Roles: []auth.RoleName{auth.RoleViewer}}))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(recorder.Body.String(), "任务已开始执行") {
		t.Fatalf("SSE 事件响应不正确：%d %s", recorder.Code, recorder.Body.String())
	}
}

func TestRunHandlerCancelsAndRetriesThroughManager(t *testing.T) {
	manager := &fakeRunManager{retryID: "run-2"}
	handler := RunHandler(manager)
	for _, test := range []struct {
		path string
		want int
	}{
		{path: "/api/runs/run-1/cancel", want: http.StatusNoContent},
		{path: "/api/runs/run-1/retry", want: http.StatusCreated},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, nil)
		request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "operator", Roles: []auth.RoleName{auth.RoleOperator}}))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatalf("%s 应返回 %d，实际 %d：%s", test.path, test.want, recorder.Code, recorder.Body.String())
		}
	}
	if manager.cancelled != "run-1" || manager.retried != "run-1" {
		t.Fatalf("取消和重试未传入服务：cancel=%s retry=%s", manager.cancelled, manager.retried)
	}
}

type fakeRunManager struct {
	runs      []RunView
	detail    RunView
	events    []RunStreamEvent
	cancelled string
	retried   string
	retryID   RunID
}

func (m *fakeRunManager) ListRuns(context.Context) ([]RunView, error) { return m.runs, nil }
func (m *fakeRunManager) GetRun(_ context.Context, id string) (RunView, error) {
	if m.detail.ID == "" {
		m.detail = RunView{ID: id}
	}
	return m.detail, nil
}
func (m *fakeRunManager) ListRunEvents(context.Context, string) ([]RunStreamEvent, error) {
	return m.events, nil
}
func (m *fakeRunManager) CancelRun(_ context.Context, id string) error {
	m.cancelled = id
	return nil
}
func (m *fakeRunManager) RetryRun(_ context.Context, id string) (RunID, error) {
	m.retried = id
	return m.retryID, nil
}
