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

func TestSecretEndpointNeverEchoesPlaintextAndOnlyAdminCanCreate(t *testing.T) {
	secrets := &fakeSecrets{}
	audits := &fakeAudits{}
	handler := securityhttp.NewHandler(securityhttp.Services{Secrets: secrets, Audits: audits})
	request := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(`{"name":"生产令牌","value":"never-echo-this"}`))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "admin-1", Roles: []auth.RoleName{auth.RoleAdmin}}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("管理员创建敏感参数应成功：code=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "never-echo-this") || strings.Contains(recorder.Body.String(), "cipher") {
		t.Fatalf("接口不得回显明文或密文材料：%s", recorder.Body.String())
	}
	if secrets.createdBy != "admin-1" || string(secrets.plaintext) != "never-echo-this" {
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

func TestRoleUpdateAndCredentialOperationsAreAudited(t *testing.T) {
	audits := &fakeAudits{}
	team := &fakeTeam{}
	credentials := &fakeCredentials{}
	handler := securityhttp.NewHandler(securityhttp.Services{Audits: audits, Team: team, Credentials: credentials})
	admin := auth.Principal{UserID: "admin-1", Roles: []auth.RoleName{auth.RoleAdmin}}

	roleRequest := httptest.NewRequest(http.MethodPut, "/api/members/user-2/roles", strings.NewReader(`{"roles":["operator"]}`))
	roleRequest = roleRequest.WithContext(auth.WithPrincipal(roleRequest.Context(), admin))
	roleRecorder := httptest.NewRecorder()
	handler.ServeHTTP(roleRecorder, roleRequest)
	if roleRecorder.Code != http.StatusOK || len(team.roles) != 1 || team.roles[0] != auth.RoleOperator {
		t.Fatalf("管理员角色更新失败：code=%d roles=%v body=%s", roleRecorder.Code, team.roles, roleRecorder.Body.String())
	}

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
	if strings.Join(actions, ",") != "member.roles.update,server.credential.rotate,server.credential.revoke" {
		t.Fatalf("关键安全操作应全部审计：%v", actions)
	}
}

func TestSecretEndpointRejectsTrailingJSON(t *testing.T) {
	secrets := &fakeSecrets{}
	handler := securityhttp.NewHandler(securityhttp.Services{Secrets: secrets, Audits: &fakeAudits{}})
	request := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(
		`{"name":"生产令牌","value":"first"} {"value":"second"}`,
	))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: "admin-1", Roles: []auth.RoleName{auth.RoleAdmin},
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

type fakeTeam struct{ roles []auth.RoleName }

func (f *fakeTeam) List(context.Context) ([]auth.Member, error) { return []auth.Member{}, nil }
func (f *fakeTeam) UpdateRoles(_ context.Context, id string, roles []auth.RoleName) (auth.Member, error) {
	f.roles = append([]auth.RoleName(nil), roles...)
	return auth.Member{ID: id, Roles: roles}, nil
}

type fakeCredentials struct{ revoked string }

func (f *fakeCredentials) Rotate(context.Context, string) (server.AgentCredentials, error) {
	return server.AgentCredentials{ServerID: "server-1", Credential: "shown-once"}, nil
}
func (f *fakeCredentials) Revoke(_ context.Context, id string) error { f.revoked = id; return nil }

type fakeAlerts struct{}

func (fakeAlerts) List(context.Context) ([]alert.Alert, error)       { return []alert.Alert{}, nil }
func (fakeAlerts) Acknowledge(context.Context, string, string) error { return nil }
