package operationshttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/backup"
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

func TestFeishuTestMessageAndDeliveryStatusAPI(t *testing.T) {
	deliveries := &deliveryManager{delivery: notification.Delivery{
		ID: "delivery-1", Status: notification.DeliveryPending, Attempts: 0,
	}}
	handler := operationshttp.NewHandler(operationshttp.Services{Deliveries: deliveries}, "https://aiwise.top")
	request := httptest.NewRequest(http.MethodPost, "/api/operations/notifications/feishu/test", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://aiwise.top")
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(auth.WithPrincipal(request.Context(), adminPrincipal()))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted || deliveries.actorID != "admin-1" {
		t.Fatalf("测试消息入队失败：status=%d actor=%q body=%s", recorder.Code, deliveries.actorID, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/operations/notifications/delivery-1", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), viewerPrincipal()))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"pending"`) {
		t.Fatalf("读取测试消息状态失败：status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBackupSummaryHistoryAndManualRequestAPIs(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	manager := &backupManager{
		summary:       backup.Summary{Status: "healthy", NextBackupAt: ptrTime(now.Add(time.Hour))},
		backups:       []backup.BackupRun{{ID: "11111111-1111-4111-8111-111111111111", Status: backup.StatusSucceeded, COSSnapshotID: "cos"}},
		verifications: []backup.RestoreVerification{{ID: "22222222-2222-4222-8222-222222222222", Status: backup.VerificationSucceeded}},
	}
	handler := operationshttp.NewHandler(operationshttp.Services{Backups: manager}, "https://aiwise.top")

	for path, fragment := range map[string]string{
		"/api/operations/summary":       `"nextBackupAt"`,
		"/api/operations/backups":       `"backups"`,
		"/api/operations/verifications": `"verifications"`,
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request = request.WithContext(auth.WithPrincipal(request.Context(), viewerPrincipal()))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), fragment) {
			t.Fatalf("GET %s 失败：status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}

	viewerRequest := httptest.NewRequest(http.MethodPost, "/api/operations/backups", strings.NewReader(`{}`))
	viewerRequest = viewerRequest.WithContext(auth.WithPrincipal(viewerRequest.Context(), viewerPrincipal()))
	viewerRecorder := httptest.NewRecorder()
	handler.ServeHTTP(viewerRecorder, viewerRequest)
	if viewerRecorder.Code != http.StatusForbidden {
		t.Fatalf("viewer 不得请求备份：%d", viewerRecorder.Code)
	}

	backupRequest := httptest.NewRequest(http.MethodPost, "/api/operations/backups", strings.NewReader(`{}`))
	backupRequest.Header.Set("Origin", "https://aiwise.top")
	backupRequest.Header.Set("Content-Type", "application/json")
	backupRequest.Header.Set("Idempotency-Key", "33333333-3333-4333-8333-333333333333")
	backupRequest = backupRequest.WithContext(auth.WithPrincipal(backupRequest.Context(), adminPrincipal()))
	backupRecorder := httptest.NewRecorder()
	handler.ServeHTTP(backupRecorder, backupRequest)
	if backupRecorder.Code != http.StatusAccepted || manager.backupKey != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("管理员备份请求失败：status=%d key=%q body=%s", backupRecorder.Code, manager.backupKey, backupRecorder.Body.String())
	}

	verificationRequest := httptest.NewRequest(http.MethodPost, "/api/operations/verifications", strings.NewReader(`{"backupRunId":"11111111-1111-4111-8111-111111111111"}`))
	verificationRequest.Header.Set("Origin", "https://aiwise.top")
	verificationRequest.Header.Set("Content-Type", "application/json")
	verificationRequest.Header.Set("Idempotency-Key", "44444444-4444-4444-8444-444444444444")
	verificationRequest = verificationRequest.WithContext(auth.WithPrincipal(verificationRequest.Context(), adminPrincipal()))
	verificationRecorder := httptest.NewRecorder()
	handler.ServeHTTP(verificationRecorder, verificationRequest)
	if verificationRecorder.Code != http.StatusAccepted || manager.verificationBackupID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("管理员恢复校验请求失败：status=%d backup=%q body=%s", verificationRecorder.Code, manager.verificationBackupID, verificationRecorder.Body.String())
	}
}

func TestBackupWritesRequireSameOriginJSONAndUUIDIdempotencyKey(t *testing.T) {
	manager := &backupManager{}
	handler := operationshttp.NewHandler(operationshttp.Services{Backups: manager}, "https://aiwise.top")
	for _, test := range []struct {
		name, origin, contentType, key string
		wantStatus                     int
	}{
		{name: "cross origin", origin: "https://evil.example", contentType: "application/json", key: "33333333-3333-4333-8333-333333333333", wantStatus: http.StatusForbidden},
		{name: "not json", origin: "https://aiwise.top", contentType: "text/plain", key: "33333333-3333-4333-8333-333333333333", wantStatus: http.StatusUnsupportedMediaType},
		{name: "missing key", origin: "https://aiwise.top", contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "invalid key", origin: "https://aiwise.top", contentType: "application/json", key: "not-a-uuid", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/operations/backups", strings.NewReader(`{}`))
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Idempotency-Key", test.key)
			request = request.WithContext(auth.WithPrincipal(request.Context(), adminPrincipal()))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

type notificationManager struct {
	view      notification.FeishuConfigView
	lastInput notification.FeishuConfigInput
	actorID   string
}

type deliveryManager struct {
	delivery notification.Delivery
	actorID  string
}

type backupManager struct {
	summary              backup.Summary
	backups              []backup.BackupRun
	verifications        []backup.RestoreVerification
	backupKey            string
	verificationBackupID string
}

func (m *backupManager) Summary(context.Context) (backup.Summary, error) { return m.summary, nil }
func (m *backupManager) ListBackups(context.Context, int) ([]backup.BackupRun, error) {
	return m.backups, nil
}
func (m *backupManager) RequestBackup(_ context.Context, _ string, key string, at time.Time) (backup.BackupRun, error) {
	m.backupKey = key
	run := backup.BackupRun{ID: "55555555-5555-4555-8555-555555555555", TriggerType: backup.TriggerManual, Status: backup.StatusQueued, CreatedAt: at}
	m.backups = append([]backup.BackupRun{run}, m.backups...)
	return run, nil
}
func (m *backupManager) ListVerifications(context.Context, int) ([]backup.RestoreVerification, error) {
	return m.verifications, nil
}
func (m *backupManager) RequestVerification(_ context.Context, _ string, backupID, _ string, at time.Time) (backup.RestoreVerification, error) {
	m.verificationBackupID = backupID
	return backup.RestoreVerification{ID: "66666666-6666-4666-8666-666666666666", BackupRunID: backupID, TriggerType: backup.TriggerManual, Status: backup.VerificationQueued, CreatedAt: at}, nil
}

func ptrTime(value time.Time) *time.Time { return &value }

func (m *deliveryManager) EnqueueTest(_ context.Context, actorID string) (notification.Delivery, error) {
	m.actorID = actorID
	return m.delivery, nil
}

func (m *deliveryManager) GetDelivery(context.Context, string) (notification.Delivery, error) {
	return m.delivery, nil
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
