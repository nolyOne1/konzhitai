package agentrelease

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
)

func TestManagementHandlerListsAndChangesReleaseStateInChinese(t *testing.T) {
	repository := newMemoryRepository()
	service := NewService(repository, newMemoryObjectStore(), time.Now)
	a, err := service.Import(context.Background(), validImportInput("0.2.0"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.Import(context.Background(), validImportInput("0.3.0"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetRecommended(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	handler := ManagementHandler(service)

	listRequest := withReleasePrincipal(httptest.NewRequest(http.MethodGet, "/api/agent-releases", nil), auth.RoleViewer)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("版本列表：status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	var listed struct {
		Releases []Release `json:"releases"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil || len(listed.Releases) != 2 {
		t.Fatalf("版本列表响应：%+v err=%v", listed, err)
	}

	forbidden := httptest.NewRecorder()
	handler.ServeHTTP(forbidden, withReleasePrincipal(httptest.NewRequest(http.MethodPost, "/api/agent-releases/"+b.ID+"/recommend", nil), auth.RoleViewer))
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("普通查看者不得推荐版本：%d", forbidden.Code)
	}

	recommendResponse := httptest.NewRecorder()
	handler.ServeHTTP(recommendResponse, withReleasePrincipal(httptest.NewRequest(http.MethodPost, "/api/agent-releases/"+b.ID+"/recommend", nil), auth.RoleAdmin))
	if recommendResponse.Code != http.StatusOK || !strings.Contains(recommendResponse.Body.String(), "0.3.0") {
		t.Fatalf("推荐版本：status=%d body=%s", recommendResponse.Code, recommendResponse.Body.String())
	}

	currentResponse := httptest.NewRecorder()
	handler.ServeHTTP(currentResponse, withReleasePrincipal(httptest.NewRequest(http.MethodPost, "/api/agent-releases/"+b.ID+"/withdraw", nil), auth.RoleAdmin))
	if currentResponse.Code != http.StatusConflict || !strings.Contains(currentResponse.Body.String(), "推荐版本") {
		t.Fatalf("撤回当前推荐版本：status=%d body=%s", currentResponse.Code, currentResponse.Body.String())
	}

	withdrawResponse := httptest.NewRecorder()
	handler.ServeHTTP(withdrawResponse, withReleasePrincipal(httptest.NewRequest(http.MethodPost, "/api/agent-releases/"+a.ID+"/withdraw", nil), auth.RoleAdmin))
	if withdrawResponse.Code != http.StatusOK || !strings.Contains(withdrawResponse.Body.String(), ReleaseStatusWithdrawn) {
		t.Fatalf("撤回旧版本：status=%d body=%s", withdrawResponse.Code, withdrawResponse.Body.String())
	}
}

func withReleasePrincipal(request *http.Request, role auth.RoleName) *http.Request {
	return request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "user-1", Roles: []auth.RoleName{role}}))
}
