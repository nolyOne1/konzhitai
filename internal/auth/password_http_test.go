package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
)

func TestPasswordHandlerRejectsUnauthenticatedRequest(t *testing.T) {
	handler, _ := newPasswordHandlerForTest(t, true)
	request := passwordRequest(`{"currentPassword":"correct-current-password","newPassword":"new-password-2026"}`)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未认证请求应返回 401，实际 %d", recorder.Code)
	}
}

func TestPasswordHandlerRejectsCrossOriginNonJSONAndUnknownFields(t *testing.T) {
	tests := []struct {
		name        string
		origin      string
		contentType string
		body        string
		wantStatus  int
	}{
		{
			name: "跨域", origin: "https://evil.example", contentType: "application/json",
			body:       `{"currentPassword":"correct-current-password","newPassword":"new-password-2026"}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name: "非 JSON", origin: "https://aiwise.top", contentType: "text/plain",
			body:       `{"currentPassword":"correct-current-password","newPassword":"new-password-2026"}`,
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name: "未知字段", origin: "https://aiwise.top", contentType: "application/json",
			body:       `{"currentPassword":"correct-current-password","newPassword":"new-password-2026","userId":"other"}`,
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, _ := newPasswordHandlerForTest(t, true)
			request := passwordRequest(test.body)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Content-Type", test.contentType)
			request = authenticatedPasswordRequest(request)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("状态码错误：want=%d got=%d body=%s", test.wantStatus, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestPasswordHandlerMapsRejectedAndRateLimitedRequests(t *testing.T) {
	tests := []struct {
		name       string
		allowed    bool
		current    string
		wantStatus int
	}{
		{name: "错误密码", allowed: true, current: "wrong-password", wantStatus: http.StatusBadRequest},
		{name: "请求限速", allowed: false, current: "correct-current-password", wantStatus: http.StatusTooManyRequests},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, _ := newPasswordHandlerForTest(t, test.allowed)
			body, err := json.Marshal(map[string]string{
				"currentPassword": test.current,
				"newPassword":     "new-password-2026",
			})
			if err != nil {
				t.Fatal(err)
			}
			request := authenticatedPasswordRequest(passwordRequest(string(body)))
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("状态码错误：want=%d got=%d body=%s", test.wantStatus, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestPasswordHandlerChangesPasswordWithoutCaching(t *testing.T) {
	handler, store := newPasswordHandlerForTest(t, true)
	request := authenticatedPasswordRequest(passwordRequest(
		`{"currentPassword":"correct-current-password","newPassword":"new-password-2026"}`,
	))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("成功改密应返回 204，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("成功响应必须禁止缓存：%q", recorder.Header().Get("Cache-Control"))
	}
	if store.commit == nil || store.commit.IPAddress != "203.0.113.8" {
		t.Fatalf("必须使用直接来源 IP：%+v", store.commit)
	}
}

func TestPasswordHandlerUsesOnlyTrustedSingleForwardedIP(t *testing.T) {
	t.Setenv("YUNLING_TRUST_PROXY", "true")
	handler, store := newPasswordHandlerForTest(t, true)
	request := authenticatedPasswordRequest(passwordRequest(
		`{"currentPassword":"correct-current-password","newPassword":"new-password-2026"}`,
	))
	request.Header.Set("X-Forwarded-For", "198.51.100.7")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("可信代理请求应成功，实际 %d：%s", recorder.Code, recorder.Body.String())
	}
	if store.commit == nil || store.commit.IPAddress != "198.51.100.7" {
		t.Fatalf("必须使用可信代理覆盖后的单值 IP：%+v", store.commit)
	}
}

func TestPasswordHandlerDoesNotMatchExtraPath(t *testing.T) {
	handler, _ := newPasswordHandlerForTest(t, true)
	request := authenticatedPasswordRequest(passwordRequest(
		`{"currentPassword":"correct-current-password","newPassword":"new-password-2026"}`,
	))
	request.URL.Path = "/api/auth/password/extra"
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("额外路径必须返回 404，实际 %d", recorder.Code)
	}
}

func newPasswordHandlerForTest(t *testing.T, allowed bool) (http.Handler, *fakePasswordChangeStore) {
	t.Helper()
	currentHash, err := auth.HashPassword("correct-current-password")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakePasswordChangeStore{passwordHash: currentHash, allowed: allowed}
	service := auth.NewPasswordChangeService(store, func() time.Time {
		return time.Date(2026, 8, 31, 5, 0, 0, 0, time.UTC)
	})
	return auth.PasswordHandler(service, "https://aiwise.top"), store
}

func passwordRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "https://aiwise.top/api/auth/password", strings.NewReader(body))
	request.Header.Set("Origin", "https://aiwise.top")
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.8:43210"
	return request
}

func authenticatedPasswordRequest(request *http.Request) *http.Request {
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "current-token"})
	ctx := auth.WithPrincipal(request.Context(), auth.Principal{UserID: "user-1"})
	return request.WithContext(ctx)
}
