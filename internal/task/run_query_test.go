package task

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
)

func TestRunQueryValidatesAndForwardsFilters(t *testing.T) {
	manager := &fakeRunManager{}
	request := httptest.NewRequest(http.MethodGet, "/api/runs?state=failed&query=archive&from=2026-08-01T00:00:00Z&until=2026-09-01T00:00:00Z&limit=25&offset=250", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Roles: []auth.RoleName{auth.RoleViewer}}))
	recorder := httptest.NewRecorder()
	RunHandler(manager).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || manager.filter.Offset != 250 || manager.filter.Limit != 25 || manager.filter.State != Failed || manager.filter.From == nil || manager.filter.Until == nil || manager.filter.Query != "archive" {
		t.Fatalf("历史筛选未传递到服务：%d %+v", recorder.Code, manager.filter)
	}
	for _, raw := range []string{"state=invalid", "limit=201", "offset=-1", "from=bad", "taskId=not-a-uuid", "from=2026-09-01T00:00:00Z&until=2026-08-01T00:00:00Z"} {
		values, _ := url.ParseQuery(raw)
		if _, err := parseRunFilter(values); err == nil {
			t.Errorf("应拒绝筛选条件 %s", raw)
		}
	}
}

func TestRunQueryEscapesWildcardsAndUsesStablePaging(t *testing.T) {
	query, args := runFilterQuery(RunFilter{Query: `100%_done`, State: Failed, Limit: 50, Offset: 200})
	if strings.Contains(query, "100%") || !strings.Contains(query, "run.state=$2") || !strings.Contains(query, "ORDER BY run.created_at DESC, run.id DESC") || args[0] != `%100\%\_done%` || args[2] != 51 || args[3] != 200 {
		t.Fatalf("筛选应使用参数绑定、字面匹配和稳定分页：%s %#v", query, args)
	}
}

func TestDownloadRunLogsUsesAllStoredChunksWithoutStateEvents(t *testing.T) {
	now := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	manager := &fakeRunManager{events: []RunStreamEvent{
		{Kind: "state", Message: "not a log", OccurredAt: now},
		{Kind: "log", Stream: "stdout", Content: "first chunk\n", OccurredAt: now},
		{Kind: "log", Stream: "stderr", Content: "last chunk", OccurredAt: now},
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/runs/run-1/logs", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Roles: []auth.RoleName{auth.RoleViewer}}))
	recorder := httptest.NewRecorder()
	RunHandler(manager).ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "first chunk\n") || !strings.HasSuffix(body, "last chunk\n") || strings.Contains(body, "not a log") || !strings.Contains(recorder.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("日志下载内容不完整：%d %s", recorder.Code, body)
	}
}

func TestUpdateScheduleBindsToTaskURL(t *testing.T) {
	manager := &fakeTaskManager{}
	request := httptest.NewRequest(http.MethodPut, "/api/tasks/task-1/schedules/schedule-1", strings.NewReader(`{"definitionId":"forged-task","cronExpression":"0 3 * * *","timezone":"UTC","enabled":false}`))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Roles: []auth.RoleName{auth.RoleOperator}}))
	recorder := httptest.NewRecorder()
	Handler(manager).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || manager.scheduleID != "schedule-1" || manager.scheduleInput.DefinitionID != "task-1" || manager.scheduleInput.Enabled {
		t.Fatalf("更新计划必须限定URL任务并保存停用状态：%d %+v", recorder.Code, manager.scheduleInput)
	}
}

func TestTaskUnlimitedWaitIsPreserved(t *testing.T) {
	input := normalizeInput(CreateInput{Name: "无限等待", ScriptID: "script", RequiredRuntime: "bash", Resources: Resources{CPUMillicores: 100, MemoryBytes: 1024, DiskBytes: 1024}})
	if input.MaxWaitSeconds != 0 || validateInput(input) != nil {
		t.Fatalf("0应保留为无限等待：%+v", input)
	}
	input.MaxWaitSeconds = -1
	if validateInput(input) == nil {
		t.Fatal("负等待时间不得接受")
	}
}
