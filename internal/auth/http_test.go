package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yunling.local/platform/internal/auth"
)

func TestLoginSetsSecureServerSessionCookie(t *testing.T) {
	hash, err := auth.HashPassword("正确密码")
	if err != nil {
		t.Fatalf("生成测试密码哈希：%v", err)
	}
	sessions := &fakeSessions{}
	service := auth.NewService(fakeUsers{user: auth.User{
		ID:           "user-1",
		Email:        "ops@example.com",
		DisplayName:  "值班运维",
		PasswordHash: hash,
		Enabled:      true,
		Roles:        []auth.RoleName{auth.RoleOperator},
	}}, sessions)
	handler := auth.Handler(service)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/login",
		strings.NewReader(`{"email":"ops@example.com","password":"正确密码"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("登录成功状态码应为 200，实际为 %d，响应 %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("登录成功应设置一个会话 Cookie，实际为 %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != auth.SessionCookieName || cookie.Value == "" {
		t.Fatalf("会话 Cookie 名称或内容无效：%+v", cookie)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("会话 Cookie 必须启用 HttpOnly、Secure 和 SameSite=Lax：%+v", cookie)
	}
	if sessions.created == nil {
		t.Fatal("登录接口应创建服务端会话")
	}
}

func TestSessionEndpointReturnsCurrentUser(t *testing.T) {
	sessions := &fakeSessions{principal: auth.Principal{
		UserID:      "user-1",
		Email:       "ops@example.com",
		DisplayName: "值班运维",
		Roles:       []auth.RoleName{auth.RoleOperator},
	}}
	service := auth.NewService(fakeUsers{}, sessions)
	handler := auth.Handler(service)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "token"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("有效会话状态码应为 200，实际为 %d", rec.Code)
	}
	var body struct {
		User auth.Principal `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析会话响应：%v", err)
	}
	if body.User.DisplayName != "值班运维" {
		t.Fatalf("会话响应应返回当前用户，实际为 %+v", body.User)
	}
}

var _ auth.UserRepository = fakeUsers{}
var _ auth.SessionRepository = (*fakeSessions)(nil)
