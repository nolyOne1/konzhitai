package operationshttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/notification"
	"yunling.local/platform/internal/operationshttp"
)

func TestFeishuConfigGETNeverReturnsSecrets(t *testing.T) {
	service := &notificationManager{view: notification.FeishuConfigView{
		Configured: true, Enabled: true, MaskedDestination: "飞书机器人 …cdef",
	}}
	handler := operationshttp.NewHandler(operationshttp.Services{Notifications: service}, "https://aiwise.top")
	request := httptest.NewRequest(http.MethodGet, "/api/operations/notifications/feishu", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), adminPrincipal()))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "open.feishu.cn") || strings.Contains(recorder.Body.String(), "signingSecret") {
		t.Fatalf("GET 响应不安全：status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("通知配置响应必须禁止缓存")
	}
}

func TestFeishuConfigPUTRequiresAdminSameOriginJSONAndExactFields(t *testing.T) {
	tests := []struct {
		name        string
		principal   auth.Principal
		origin      string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "viewer", principal: viewerPrincipal(), origin: "https://aiwise.top", contentType: "application/json", body: validUpdateBody(), wantStatus: http.StatusForbidden},
		{name: "cross origin", principal: adminPrincipal(), origin: "https://evil.example", contentType: "application/json", body: validUpdateBody(), wantStatus: http.StatusForbidden},
		{name: "non json", principal: adminPrincipal(), origin: "https://aiwise.top", contentType: "text/plain", body: validUpdateBody(), wantStatus: http.StatusUnsupportedMediaType},
		{name: "unknown field", principal: adminPrincipal(), origin: "https://aiwise.top", contentType: "application/json", body: strings.TrimSuffix(validUpdateBody(), "}") + `,"ciphertext":"leak"}`, wantStatus: http.StatusBadRequest},
		{name: "admin", principal: adminPrincipal(), origin: "https://aiwise.top", contentType: "application/json; charset=utf-8", body: validUpdateBody(), wantStatus: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &notificationManager{view: notification.FeishuConfigView{Configured: true, Enabled: true, MaskedDestination: "飞书机器人 …cdef"}}
			handler := operationshttp.NewHandler(operationshttp.Services{Notifications: service}, "https://aiwise.top")
			request := httptest.NewRequest(http.MethodPut, "/api/operations/notifications/feishu", strings.NewReader(test.body))
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Content-Type", test.contentType)
			request = request.WithContext(auth.WithPrincipal(request.Context(), test.principal))
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if test.wantStatus == http.StatusOK {
				if service.lastInput.Webhook != validWebhook || service.actorID != "admin-1" {
					t.Fatalf("更新参数错误：input=%+v actor=%q", service.lastInput, service.actorID)
				}
				var response map[string]any
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if _, exists := response["webhook"]; exists {
					t.Fatal("响应不得包含 Webhook")
				}
			}
		})
	}
}

type notificationManager struct {
	view      notification.FeishuConfigView
	lastInput notification.FeishuConfigInput
	actorID   string
}

func (m *notificationManager) Get(context.Context) (notification.FeishuConfigView, error) {
	return m.view, nil
}

func (m *notificationManager) Update(_ context.Context, actorID, _ string, input notification.FeishuConfigInput) (notification.FeishuConfigView, error) {
	m.actorID, m.lastInput = actorID, input
	return m.view, nil
}

func adminPrincipal() auth.Principal {
	return auth.Principal{UserID: "admin-1", Roles: []auth.RoleName{auth.RoleAdmin}}
}

func viewerPrincipal() auth.Principal {
	return auth.Principal{UserID: "viewer-1", Roles: []auth.RoleName{auth.RoleViewer}}
}

func validUpdateBody() string {
	return `{"enabled":true,"webhook":"` + validWebhook + `","signingSecret":"signing-secret"}`
}

const validWebhook = "https://open.feishu.cn/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef"
