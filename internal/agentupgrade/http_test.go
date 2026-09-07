package agentupgrade

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yunling.local/platform/internal/auth"
)

func TestUpgradeManagementHandlerCreatesAndProtectsPlans(t *testing.T) {
	service := NewService(validMemoryRepository())
	handler := ManagementHandler(service)
	body := `{"target_release_id":"release-2","server_ids":["s1"],"batch_size":1}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, upgradeRequest(http.MethodPost, "/api/agent-upgrades", body, auth.RoleAdmin))
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"status":"running"`) {
		t.Fatalf("创建升级计划响应错误：%d %s", response.Code, response.Body.String())
	}
	forbidden := httptest.NewRecorder()
	handler.ServeHTTP(forbidden, upgradeRequest(http.MethodPost, "/api/agent-upgrades", body, auth.RoleViewer))
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("查看者不得创建升级计划：%d", forbidden.Code)
	}
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, upgradeRequest(http.MethodGet, "/api/agent-upgrades", "", auth.RoleViewer))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"plans":[`) {
		t.Fatalf("计划列表错误：%d %s", list.Code, list.Body.String())
	}
}

func TestUpgradeManagementHandlerMapsMalformedAndMissing(t *testing.T) {
	handler := ManagementHandler(NewService(validMemoryRepository()))
	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, upgradeRequest(http.MethodPost, "/api/agent-upgrades", "{", auth.RoleAdmin))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("错误 JSON 状态码：%d", bad.Code)
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, upgradeRequest(http.MethodGet, "/api/agent-upgrades/missing", "", auth.RoleViewer))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("缺失计划状态码：%d", missing.Code)
	}
}

func upgradeRequest(method, path, body string, role auth.RoleName) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	return request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "user-1", Roles: []auth.RoleName{role}}))
}
