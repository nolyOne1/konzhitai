package task

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"yunling.local/platform/internal/auth"
)

func TestManualRunUsesAuthenticatedOperatorAsRequester(t *testing.T) {
	manager := &fakeTaskManager{run: Run{ID: "run-1", DefinitionID: "task-1", ScriptVersionID: "version-1", State: Queued}}
	handler := Handler(manager)
	request := httptest.NewRequest(http.MethodPost, "/api/tasks/task-1/run", bytes.NewBufferString(`{"parameters":{"日期":"2026-08-28"}}`))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: "operator-1",
		Roles:  []auth.RoleName{auth.RoleOperator},
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("手动执行应返回 201，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if manager.triggeredDefinitionID != "task-1" || manager.trigger.RequestedBy != "operator-1" || manager.trigger.Type != TriggerManual {
		t.Fatalf("手动执行必须使用登录操作者：definition=%s trigger=%+v", manager.triggeredDefinitionID, manager.trigger)
	}
	if manager.trigger.Parameters["日期"] != "2026-08-28" {
		t.Fatalf("手动参数未传入任务服务：%+v", manager.trigger.Parameters)
	}
}

func TestManualRunRejectsForgedRequesterField(t *testing.T) {
	manager := &fakeTaskManager{}
	handler := Handler(manager)
	request := httptest.NewRequest(http.MethodPost, "/api/tasks/task-1/run", bytes.NewBufferString(`{"requestedBy":"forged-user"}`))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: "operator-1",
		Roles:  []auth.RoleName{auth.RoleOperator},
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("伪造执行人字段必须被拒绝，实际 %d", recorder.Code)
	}
	if manager.triggeredDefinitionID != "" {
		t.Fatal("无效请求不得进入任务服务")
	}
}

func TestDisableEndpointDoesNotCancelQueuedRunsUnlessRequested(t *testing.T) {
	manager := &fakeTaskManager{}
	handler := Handler(manager)
	request := httptest.NewRequest(http.MethodPatch, "/api/tasks/task-1/enabled", bytes.NewBufferString(`{"enabled":false}`))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: "operator-1",
		Roles:  []auth.RoleName{auth.RoleOperator},
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("停用任务应返回 204，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if manager.enabled || manager.cancelQueued {
		t.Fatalf("默认停用不得取消排队实例：enabled=%v cancelQueued=%v", manager.enabled, manager.cancelQueued)
	}
}

func TestCreateTaskIgnoresForgedCreatorAndReturnsChineseJSON(t *testing.T) {
	manager := &fakeTaskManager{definition: Definition{ID: "task-1", Name: "每日归档", ScriptID: "script-1", Enabled: true}}
	handler := Handler(manager)
	request := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewBufferString(`{
		"name":"每日归档",
		"scriptId":"script-1",
		"versionPolicy":"latest",
		"requiredRuntime":"bash",
		"resources":{"cpuMillicores":100,"memoryBytes":134217728,"diskBytes":134217728},
		"priority":50,
		"maxConcurrency":1,
		"timeoutSeconds":3600,
		"maxWaitSeconds":86400,
		"enabled":true,
		"createdBy":"forged-user"
	}`))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: "operator-1",
		Roles:  []auth.RoleName{auth.RoleOperator},
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("创建任务应返回 201，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if manager.created.CreatedBy != "operator-1" {
		t.Fatalf("创建人必须来自登录上下文，实际 %q", manager.created.CreatedBy)
	}
	var response Definition
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Name != "每日归档" {
		t.Fatalf("创建接口响应格式不正确：response=%+v err=%v", response, err)
	}
}

type fakeTaskManager struct {
	definition            Definition
	definitions           []Definition
	run                   Run
	schedules             []Schedule
	created               CreateInput
	triggeredDefinitionID string
	trigger               Trigger
	enabled               bool
	cancelQueued          bool
}

func (m *fakeTaskManager) Create(_ context.Context, input CreateInput) (Definition, error) {
	m.created = input
	return m.definition, nil
}

func (m *fakeTaskManager) Update(_ context.Context, _ string, input CreateInput) (Definition, error) {
	m.created = input
	return m.definition, nil
}

func (m *fakeTaskManager) Get(_ context.Context, _ string) (Definition, error) {
	return m.definition, nil
}

func (m *fakeTaskManager) List(context.Context) ([]Definition, error) {
	return m.definitions, nil
}

func (m *fakeTaskManager) Delete(context.Context, string) error { return nil }

func (m *fakeTaskManager) SetEnabled(_ context.Context, _ string, enabled, cancelQueued bool) error {
	m.enabled = enabled
	m.cancelQueued = cancelQueued
	return nil
}

func (m *fakeTaskManager) Trigger(_ context.Context, definitionID string, trigger Trigger) (Run, error) {
	m.triggeredDefinitionID = definitionID
	m.trigger = trigger
	return m.run, nil
}

func (m *fakeTaskManager) CreateSchedule(context.Context, ScheduleInput) (Schedule, error) {
	return Schedule{}, nil
}

func (m *fakeTaskManager) ListSchedules(context.Context, string) ([]Schedule, error) {
	return m.schedules, nil
}

func (m *fakeTaskManager) DeleteSchedule(context.Context, string, string) error { return nil }
