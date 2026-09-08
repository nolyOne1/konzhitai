package auth_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"yunling.local/platform/internal/auth"
)

func TestOperatorCannotPublishScript(t *testing.T) {
	if !auth.RoleOperator.Allows(auth.PermissionExecute) {
		t.Fatal("运维人员应能执行任务")
	}
	if auth.RoleOperator.Allows(auth.PermissionPublishScript) {
		t.Fatal("运维人员不应能发布脚本")
	}
}

func TestWrongPasswordReturnsInvalidCredentials(t *testing.T) {
	hash, err := auth.HashPassword("正确密码")
	if err != nil {
		t.Fatalf("生成测试密码哈希：%v", err)
	}
	users := fakeUsers{user: auth.User{
		ID:           "user-1",
		Email:        "ops@example.com",
		PasswordHash: hash,
		Enabled:      true,
		Roles:        []auth.RoleName{auth.RoleOperator},
	}}
	svc := auth.NewService(users, &fakeSessions{})

	_, err = svc.Login(context.Background(), "ops@example.com", "错误密码")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("错误密码应返回 ErrInvalidCredentials，实际为 %v", err)
	}
}

func TestLoginCreatesHashedServerSession(t *testing.T) {
	hash, err := auth.HashPassword("正确密码")
	if err != nil {
		t.Fatalf("生成测试密码哈希：%v", err)
	}
	users := fakeUsers{user: auth.User{
		ID:           "user-1",
		Email:        "ops@example.com",
		DisplayName:  "值班运维",
		PasswordHash: hash,
		Enabled:      true,
		Roles:        []auth.RoleName{auth.RoleOperator},
	}}
	sessions := &fakeSessions{}
	svc := auth.NewService(users, sessions)

	session, err := svc.Login(context.Background(), "OPS@example.com", "正确密码")
	if err != nil {
		t.Fatalf("正确凭据登录失败：%v", err)
	}
	if session.Token == "" {
		t.Fatal("登录成功后应返回随机会话令牌")
	}
	if sessions.created == nil {
		t.Fatal("登录成功后应保存服务端会话")
	}
	expectedHash := sha256.Sum256([]byte(session.Token))
	if string(sessions.created.TokenHash) != string(expectedHash[:]) {
		t.Fatal("数据库中应仅保存会话令牌哈希")
	}
	if string(sessions.created.TokenHash) == session.Token {
		t.Fatal("数据库中不得保存明文会话令牌")
	}
}

func TestLoginPropagatesMustChangePasswordToSession(t *testing.T) {
	hash, err := auth.HashPassword("temporary-password")
	if err != nil {
		t.Fatalf("生成测试密码哈希：%v", err)
	}
	users := fakeUsers{user: auth.User{
		ID:                 "user-1",
		Email:              "ops@example.com",
		PasswordHash:       hash,
		Enabled:            true,
		MustChangePassword: true,
	}}
	service := auth.NewService(users, &fakeSessions{})

	session, err := service.Login(context.Background(), "ops@example.com", "temporary-password")
	if err != nil {
		t.Fatalf("临时密码登录失败：%v", err)
	}
	if !session.MustChangePassword {
		t.Fatal("登录会话必须携带首次改密标记")
	}
}

func TestRequireRejectsRoleWithoutPermission(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := auth.Require(auth.PermissionPublishScript)(next)
	req := httptest.NewRequest(http.MethodPost, "/api/scripts/publish", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "user-1",
		Roles:  []auth.RoleName{auth.RoleOperator},
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺少权限时状态码应为 403，实际为 %d", rec.Code)
	}
	if rec.Body.String() != "{\"message\":\"没有执行此操作的权限\"}\n" {
		t.Fatalf("缺少权限时应返回中文错误，实际为 %q", rec.Body.String())
	}
}

func TestAuthenticateLoadsServerSessionBeforePermissionCheck(t *testing.T) {
	sessions := &fakeSessions{principal: auth.Principal{
		UserID: "user-1",
		Roles:  []auth.RoleName{auth.RoleOperator},
	}}
	service := auth.NewService(fakeUsers{}, sessions)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := auth.Authenticate(service)(auth.Require(auth.PermissionExecute)(next))
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/run", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "valid-session-token"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("有效运维会话应通过执行权限校验，实际状态码为 %d", rec.Code)
	}
}

func TestAuthenticateAllowsOnlyPasswordChangeWhenRequired(t *testing.T) {
	service := authenticatedService(auth.Principal{UserID: "user-1", MustChangePassword: true})
	protected := auth.Authenticate(service)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	blocked := requestWithSession(http.MethodGet, "/api/members")
	blockedRecorder := httptest.NewRecorder()
	protected.ServeHTTP(blockedRecorder, blocked)
	if blockedRecorder.Code != http.StatusForbidden || blockedRecorder.Body.String() != "{\"code\":\"password_change_required\",\"message\":\"请先修改临时密码\"}\n" {
		t.Fatalf("临时密码会话未被门禁：%d %s", blockedRecorder.Code, blockedRecorder.Body.String())
	}

	allowed := requestWithSession(http.MethodPost, "/api/auth/password")
	allowedRecorder := httptest.NewRecorder()
	protected.ServeHTTP(allowedRecorder, allowed)
	if allowedRecorder.Code != http.StatusNoContent {
		t.Fatalf("改密接口被错误阻止：%d", allowedRecorder.Code)
	}
}

type requiredPasswordRepository struct{ principal auth.Principal }

func (r *requiredPasswordRepository) FindByEmail(context.Context, string) (auth.User, error) {
	return auth.User{}, auth.ErrUserNotFound
}

func (r *requiredPasswordRepository) Create(context.Context, auth.StoredSession) error { return nil }

func (r *requiredPasswordRepository) FindPrincipal(context.Context, []byte) (auth.Principal, error) {
	return r.principal, nil
}

func (r *requiredPasswordRepository) Revoke(context.Context, []byte) error { return nil }

func authenticatedService(principal auth.Principal) *auth.Service {
	repository := &requiredPasswordRepository{principal: principal}
	return auth.NewService(repository, repository)
}

func requestWithSession(method, target string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session-token"})
	return request
}

type fakeUsers struct {
	user auth.User
}

func (f fakeUsers) FindByEmail(_ context.Context, email string) (auth.User, error) {
	if email != f.user.Email {
		return auth.User{}, auth.ErrUserNotFound
	}
	return f.user, nil
}

type fakeSessions struct {
	created   *auth.StoredSession
	principal auth.Principal
}

func (f *fakeSessions) Create(_ context.Context, session auth.StoredSession) error {
	f.created = &session
	return nil
}

func (f *fakeSessions) FindPrincipal(_ context.Context, _ []byte) (auth.Principal, error) {
	if f.principal.UserID == "" {
		return auth.Principal{}, auth.ErrSessionNotFound
	}
	return f.principal, nil
}

func (f *fakeSessions) Revoke(_ context.Context, _ []byte) error {
	return nil
}
