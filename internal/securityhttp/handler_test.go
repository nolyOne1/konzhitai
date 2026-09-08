package securityhttp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/alert"
	"yunling.local/platform/internal/audit"
	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/secret"
	"yunling.local/platform/internal/securityhttp"
	"yunling.local/platform/internal/server"
)

const (
	handlerActorID   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	handlerTargetID  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	handlerMissingID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func TestSecretEndpointNeverEchoesPlaintextAndOnlyAdminCanCreate(t *testing.T) {
	secrets := &fakeSecrets{}
	audits := &fakeAudits{}
	handler := securityhttp.NewHandler(securityhttp.Services{Secrets: secrets, Audits: audits})
	request := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(`{"name":"生产令牌","value":"never-echo-this"}`))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: handlerActorID, Roles: []auth.RoleName{auth.RoleAdmin}}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("管理员创建敏感参数应成功：code=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "never-echo-this") || strings.Contains(recorder.Body.String(), "cipher") {
		t.Fatalf("接口不得回显明文或密文材料：%s", recorder.Body.String())
	}
	if secrets.createdBy != handlerActorID || string(secrets.plaintext) != "never-echo-this" {
		t.Fatalf("创建调用内容不正确：createdBy=%s plaintext=%q", secrets.createdBy, secrets.plaintext)
	}
	if len(audits.events) != 1 || audits.events[0].Action != "secret.create" {
		t.Fatalf("密钥创建必须写审计：%+v", audits.events)
	}

	forbidden := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(`{"name":"越权","value":"x"}`))
	forbidden = forbidden.WithContext(auth.WithPrincipal(forbidden.Context(), auth.Principal{UserID: "viewer-1", Roles: []auth.RoleName{auth.RoleViewer}}))
	forbiddenRecorder := httptest.NewRecorder()
	handler.ServeHTTP(forbiddenRecorder, forbidden)
	if forbiddenRecorder.Code != http.StatusForbidden {
		t.Fatalf("只读成员创建敏感参数应返回 403，实际为 %d", forbiddenRecorder.Code)
	}
}

func TestMemberRoleUpdateUsesLifecycleServiceWithoutSeparateAudit(t *testing.T) {
	audits := &fakeAudits{}
	team := &fakeTeam{}
	handler := securityhttp.NewHandler(securityhttp.Services{Audits: audits, Team: team})

	roleRequest := adminRequest(http.MethodPut, "/api/members/"+handlerTargetID+"/roles", `{"roles":["operator"]}`)
	roleRecorder := httptest.NewRecorder()
	handler.ServeHTTP(roleRecorder, roleRequest)
	if roleRecorder.Code != http.StatusOK || len(team.roles) != 1 || team.roles[0] != auth.RoleOperator || team.actorID != handlerActorID {
		t.Fatalf("管理员角色更新失败：code=%d roles=%v actor=%s body=%s", roleRecorder.Code, team.roles, team.actorID, roleRecorder.Body.String())
	}
	if len(audits.events) != 0 {
		t.Fatalf("成员操作由事务写入审计，HTTP 层不得重复记录：%+v", audits.events)
	}
}

func TestCredentialOperationsAreAudited(t *testing.T) {
	audits := &fakeAudits{}
	credentials := &fakeCredentials{}
	handler := securityhttp.NewHandler(securityhttp.Services{Audits: audits, Credentials: credentials})
	admin := auth.Principal{UserID: handlerActorID, Roles: []auth.RoleName{auth.RoleAdmin}}

	rotateRequest := httptest.NewRequest(http.MethodPost, "/api/servers/server-1/credentials/rotate", nil)
	rotateRequest = rotateRequest.WithContext(auth.WithPrincipal(rotateRequest.Context(), admin))
	rotateRecorder := httptest.NewRecorder()
	handler.ServeHTTP(rotateRecorder, rotateRequest)
	if rotateRecorder.Code != http.StatusCreated || !strings.Contains(rotateRecorder.Body.String(), "shown-once") || rotateRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("轮换凭据必须仅显示一次且禁止缓存：code=%d headers=%v body=%s", rotateRecorder.Code, rotateRecorder.Header(), rotateRecorder.Body.String())
	}

	revokeRequest := httptest.NewRequest(http.MethodPost, "/api/servers/server-1/credentials/revoke", nil)
	revokeRequest = revokeRequest.WithContext(auth.WithPrincipal(revokeRequest.Context(), admin))
	revokeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(revokeRecorder, revokeRequest)
	if revokeRecorder.Code != http.StatusNoContent || credentials.revoked != "server-1" {
		t.Fatalf("紧急吊销失败：code=%d revoked=%s", revokeRecorder.Code, credentials.revoked)
	}
	actions := []string{}
	for _, event := range audits.events {
		actions = append(actions, event.Action)
	}
	if strings.Join(actions, ",") != "server.credential.rotate,server.credential.revoke" {
		t.Fatalf("关键安全操作应全部审计：%v", actions)
	}
}

func TestMemberListFiltersLifecycleStatus(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
		want   auth.MemberStatus
	}{
		{name: "defaults to all", target: "/api/members", want: auth.MemberStatusAll},
		{name: "filters active", target: "/api/members?status=active", want: auth.MemberStatusActive},
		{name: "filters disabled", target: "/api/members?status=disabled", want: auth.MemberStatusDisabled},
		{name: "filters removed", target: "/api/members?status=removed", want: auth.MemberStatusRemoved},
		{name: "filters all", target: "/api/members?status=all", want: auth.MemberStatusAll},
	} {
		t.Run(test.name, func(t *testing.T) {
			team := &fakeTeam{members: []auth.Member{{ID: handlerTargetID, Email: "ops@example.com"}}}
			handler := securityhttp.NewHandler(securityhttp.Services{Team: team})
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "viewer-1", Roles: []auth.RoleName{auth.RoleViewer}}))
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK || team.status != test.want || !strings.Contains(recorder.Body.String(), "ops@example.com") {
				t.Fatalf("成员列表过滤错误：code=%d status=%q body=%s", recorder.Code, team.status, recorder.Body.String())
			}
		})
	}

	team := &fakeTeam{}
	handler := securityhttp.NewHandler(securityhttp.Services{Team: team})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, adminRequest(http.MethodGet, "/api/members?status=unknown", ""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("未知成员状态应返回 400，实际为 %d", recorder.Code)
	}
}

func TestMemberCreateReturnsTemporaryPasswordOnceWithoutAuditEcho(t *testing.T) {
	team := &fakeTeam{createResult: auth.CreateMemberResult{
		Member:            auth.Member{ID: handlerTargetID, Email: "ops@example.com"},
		TemporaryPassword: "temporary-password",
	}}
	audits := &fakeAudits{}
	handler := securityhttp.NewHandler(securityhttp.Services{Team: team, Audits: audits})
	request := adminRequest(http.MethodPost, "/api/members", `{"email":"ops@example.com","displayName":"值班运维","roles":["operator"]}`)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("创建响应错误：code=%d headers=%v", recorder.Code, recorder.Header())
	}
	if !strings.Contains(recorder.Body.String(), "temporary-password") {
		t.Fatal("没有返回一次性临时密码")
	}
	if team.actorID != handlerActorID || team.input.Email != "ops@example.com" || len(audits.events) != 0 {
		t.Fatalf("创建成员调用或审计错误：actor=%s input=%+v audits=%+v", team.actorID, team.input, audits.events)
	}
}

func TestMemberLifecycleMutationsUseActorAndResponseContracts(t *testing.T) {
	for _, test := range []struct {
		name       string
		method     string
		target     string
		body       string
		wantStatus int
		operation  string
		bodyWant   string
		noStore    bool
	}{
		{name: "replace roles", method: http.MethodPut, target: "/api/members/" + handlerTargetID + "/roles", body: `{"roles":["operator"]}`, wantStatus: http.StatusOK, operation: "roles", bodyWant: handlerTargetID},
		{name: "enable", method: http.MethodPost, target: "/api/members/" + handlerTargetID + "/enable", wantStatus: http.StatusOK, operation: "enable", bodyWant: handlerTargetID},
		{name: "disable", method: http.MethodPost, target: "/api/members/" + handlerTargetID + "/disable", wantStatus: http.StatusOK, operation: "disable", bodyWant: handlerTargetID},
		{name: "remove", method: http.MethodDelete, target: "/api/members/" + handlerTargetID, wantStatus: http.StatusNoContent, operation: "remove"},
		{name: "restore", method: http.MethodPost, target: "/api/members/" + handlerTargetID + "/restore", wantStatus: http.StatusOK, operation: "restore", bodyWant: handlerTargetID},
		{name: "reset password", method: http.MethodPost, target: "/api/members/" + handlerTargetID + "/password/reset", wantStatus: http.StatusOK, operation: "reset", bodyWant: "temporary-password", noStore: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			team := &fakeTeam{resetResult: auth.ResetPasswordResult{Member: auth.Member{ID: handlerTargetID}, TemporaryPassword: "temporary-password"}}
			handler := securityhttp.NewHandler(securityhttp.Services{Team: team})
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, adminRequest(test.method, test.target, test.body))

			if recorder.Code != test.wantStatus || team.actorID != handlerActorID || team.operation != test.operation {
				t.Fatalf("成员操作错误：code=%d actor=%s operation=%s body=%s", recorder.Code, team.actorID, team.operation, recorder.Body.String())
			}
			if test.bodyWant != "" && !strings.Contains(recorder.Body.String(), test.bodyWant) {
				t.Fatalf("成员操作响应缺少 %q：%s", test.bodyWant, recorder.Body.String())
			}
			if test.noStore && recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("一次性密码响应必须禁止缓存：headers=%v", recorder.Header())
			}
		})
	}
}

func TestMemberWritesRejectUnauthorizedInputAndDomainErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		method     string
		target     string
		body       string
		principal  auth.Principal
		team       *fakeTeam
		wantStatus int
	}{
		{name: "non admin write", method: http.MethodPost, target: "/api/members/" + handlerTargetID + "/disable", principal: auth.Principal{UserID: "viewer-1", Roles: []auth.RoleName{auth.RoleViewer}}, team: &fakeTeam{}, wantStatus: http.StatusForbidden},
		{name: "invalid create input", method: http.MethodPost, target: "/api/members", body: `{"email":"invalid","displayName":"","roles":[]}`, principal: auth.Principal{UserID: handlerActorID, Roles: []auth.RoleName{auth.RoleAdmin}}, team: &fakeTeam{}, wantStatus: http.StatusBadRequest},
		{name: "invalid roles", method: http.MethodPut, target: "/api/members/" + handlerTargetID + "/roles", body: `{"roles":[]}`, principal: auth.Principal{UserID: handlerActorID, Roles: []auth.RoleName{auth.RoleAdmin}}, team: &fakeTeam{updateErr: auth.ErrInvalidRoles}, wantStatus: http.StatusBadRequest},
		{name: "missing member", method: http.MethodPost, target: "/api/members/" + handlerMissingID + "/enable", principal: auth.Principal{UserID: handlerActorID, Roles: []auth.RoleName{auth.RoleAdmin}}, team: &fakeTeam{setEnabledErr: auth.ErrMemberNotFound}, wantStatus: http.StatusNotFound},
		{name: "duplicate email", method: http.MethodPost, target: "/api/members", body: `{"email":"ops@example.com","displayName":"值班运维","roles":["operator"]}`, principal: auth.Principal{UserID: handlerActorID, Roles: []auth.RoleName{auth.RoleAdmin}}, team: &fakeTeam{createErr: auth.ErrDuplicateEmail}, wantStatus: http.StatusConflict},
		{name: "state conflict", method: http.MethodPost, target: "/api/members/" + handlerTargetID + "/disable", principal: auth.Principal{UserID: handlerActorID, Roles: []auth.RoleName{auth.RoleAdmin}}, team: &fakeTeam{setEnabledErr: auth.ErrMemberStateConflict}, wantStatus: http.StatusConflict},
		{name: "self mutation", method: http.MethodDelete, target: "/api/members/" + handlerActorID, principal: auth.Principal{UserID: handlerActorID, Roles: []auth.RoleName{auth.RoleAdmin}}, team: &fakeTeam{removeErr: auth.ErrCannotModifySelf}, wantStatus: http.StatusConflict},
		{name: "last admin", method: http.MethodPost, target: "/api/members/" + handlerTargetID + "/restore", principal: auth.Principal{UserID: handlerActorID, Roles: []auth.RoleName{auth.RoleAdmin}}, team: &fakeTeam{restoreErr: auth.ErrLastAdmin}, wantStatus: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := securityhttp.NewHandler(securityhttp.Services{Team: test.team})
			request := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
			request = request.WithContext(auth.WithPrincipal(request.Context(), test.principal))
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("状态码错误：want=%d got=%d body=%s", test.wantStatus, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestMemberMutationRejectsInvalidPathUUIDBeforeCallingService(t *testing.T) {
	team := &fakeTeam{}
	handler := securityhttp.NewHandler(securityhttp.Services{Team: team})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, adminRequest(http.MethodPost, "/api/members/not-a-uuid/disable", ""))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法成员 UUID 应返回 400：code=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if team.operation != "" {
		t.Fatalf("非法 UUID 不得调用成员服务：operation=%q", team.operation)
	}
}

func TestSecretEndpointRejectsTrailingJSON(t *testing.T) {
	secrets := &fakeSecrets{}
	handler := securityhttp.NewHandler(securityhttp.Services{Secrets: secrets, Audits: &fakeAudits{}})
	request := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(
		`{"name":"生产令牌","value":"first"} {"value":"second"}`,
	))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: handlerActorID, Roles: []auth.RoleName{auth.RoleAdmin},
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest || secrets.plaintext != nil {
		t.Fatalf("尾随 JSON 必须在调用敏感参数服务前被拒绝：code=%d plaintext=%q", recorder.Code, secrets.plaintext)
	}
}

type fakeSecrets struct {
	createdBy string
	plaintext []byte
}

func (f *fakeSecrets) Create(ctx context.Context, name string, plaintext []byte) (secret.Metadata, error) {
	f.plaintext = append([]byte(nil), plaintext...)
	createdBy, _ := secret.CreatorFromContext(ctx)
	metadata := secret.Metadata{ID: "secret-1", Name: name, CreatedBy: createdBy, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.createdBy = metadata.CreatedBy
	return metadata, nil
}

func (f *fakeSecrets) List(context.Context) ([]secret.Metadata, error) {
	return []secret.Metadata{}, nil
}

type fakeAudits struct{ events []audit.Event }

func (f *fakeAudits) Record(_ context.Context, event audit.Event) error {
	f.events = append(f.events, event)
	return nil
}

func (f *fakeAudits) List(context.Context, audit.Filter) ([]audit.Event, error) {
	return append([]audit.Event(nil), f.events...), nil
}

type fakeTeam struct {
	members       []auth.Member
	status        auth.MemberStatus
	roles         []auth.RoleName
	actorID       string
	operation     string
	input         auth.CreateMemberInput
	createResult  auth.CreateMemberResult
	resetResult   auth.ResetPasswordResult
	listErr       error
	createErr     error
	updateErr     error
	setEnabledErr error
	removeErr     error
	restoreErr    error
	resetErr      error
}

func (f *fakeTeam) List(_ context.Context, status auth.MemberStatus) ([]auth.Member, error) {
	f.status = status
	return append([]auth.Member(nil), f.members...), f.listErr
}

func (f *fakeTeam) Create(_ context.Context, actorID string, input auth.CreateMemberInput) (auth.CreateMemberResult, error) {
	f.actorID, f.operation, f.input = actorID, "create", input
	return f.createResult, f.createErr
}

func (f *fakeTeam) UpdateRoles(_ context.Context, actorID, id string, roles []auth.RoleName) (auth.Member, error) {
	f.actorID, f.operation, f.roles = actorID, "roles", append([]auth.RoleName(nil), roles...)
	return auth.Member{ID: id, Roles: roles}, f.updateErr
}

func (f *fakeTeam) SetEnabled(_ context.Context, actorID, id string, enabled bool) (auth.Member, error) {
	f.actorID = actorID
	if enabled {
		f.operation = "enable"
	} else {
		f.operation = "disable"
	}
	return auth.Member{ID: id, Enabled: enabled}, f.setEnabledErr
}

func (f *fakeTeam) Remove(_ context.Context, actorID, id string) (auth.Member, error) {
	f.actorID, f.operation = actorID, "remove"
	return auth.Member{ID: id}, f.removeErr
}

func (f *fakeTeam) Restore(_ context.Context, actorID, id string) (auth.Member, error) {
	f.actorID, f.operation = actorID, "restore"
	return auth.Member{ID: id}, f.restoreErr
}

func (f *fakeTeam) ResetPassword(_ context.Context, actorID, _ string) (auth.ResetPasswordResult, error) {
	f.actorID, f.operation = actorID, "reset"
	return f.resetResult, f.resetErr
}

func adminRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	return request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: handlerActorID, Roles: []auth.RoleName{auth.RoleAdmin},
	}))
}

type fakeCredentials struct{ revoked string }

func (f *fakeCredentials) Rotate(context.Context, string) (server.AgentCredentials, error) {
	return server.AgentCredentials{ServerID: "server-1", Credential: "shown-once"}, nil
}
func (f *fakeCredentials) Revoke(_ context.Context, id string) error { f.revoked = id; return nil }

type fakeAlerts struct{}

func (fakeAlerts) List(context.Context) ([]alert.Alert, error)       { return []alert.Alert{}, nil }
func (fakeAlerts) Acknowledge(context.Context, string, string) error { return nil }
