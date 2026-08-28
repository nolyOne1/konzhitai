package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/auth"
)

func TestAgentConnectAuthenticatesAndOverridesClaimedServerID(t *testing.T) {
	repository := newMemoryServerRepository()
	repository.saved = make(chan struct{}, 1)
	registry := NewRegistry(repository, time.Now)
	enrollment := &fakeEnrollmentManager{serverID: "server-authenticated"}
	server := httptest.NewServer(Handler(registry, enrollment))
	defer server.Close()

	header := http.Header{}
	header.Set("Authorization", "Bearer valid-agent-credential")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(
		ctx,
		"ws"+strings.TrimPrefix(server.URL, "http")+"/api/agent/connect",
		&websocket.DialOptions{HTTPHeader: header},
	)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("建立代理 WebSocket 连接：status=%d err=%v", status, err)
	}
	defer connection.CloseNow()

	err = wsjson.Write(ctx, connection, agentprotocol.Heartbeat{
		ServerID:     "server-forged",
		Sequence:     1,
		CPUUsedMilli: 1800,
	})
	if err != nil {
		t.Fatalf("发送代理心跳：%v", err)
	}
	select {
	case <-repository.saved:
	case <-ctx.Done():
		t.Fatal("中央服务未在 3 秒内保存代理心跳")
	}

	if repository.snapshot.ServerID != "server-authenticated" {
		t.Fatalf("中央服务必须使用凭据绑定的服务器 ID，实际为 %s", repository.snapshot.ServerID)
	}
	if repository.snapshot.CPUUsedMilli != 1800 {
		t.Fatalf("中央服务未保存心跳资源值：%+v", repository.snapshot)
	}
}

func TestDisabledServerClosesCurrentAgentConnection(t *testing.T) {
	repository := newMemoryServerRepository()
	registry := NewRegistry(repository, time.Now)
	hub := NewAgentConnectionHub()
	enrollment := &fakeEnrollmentManager{serverID: "server-disabled"}
	server := httptest.NewServer(Handler(registry, enrollment, WithConnectionHub(hub)))
	defer server.Close()

	header := http.Header{}
	header.Set("Authorization", "Bearer valid-agent-credential")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(
		ctx,
		"ws"+strings.TrimPrefix(server.URL, "http")+"/api/agent/connect",
		&websocket.DialOptions{HTTPHeader: header},
	)
	if err != nil {
		t.Fatalf("建立代理 WebSocket 连接：%v", err)
	}
	defer connection.CloseNow()

	hub.SetEnabled("server-disabled", false)
	_, _, err = connection.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("停用服务器应断开代理连接，实际错误为 %v", err)
	}
}

func TestCreateEnrollmentTokenRequiresAdministrator(t *testing.T) {
	registry := NewRegistry(newMemoryServerRepository(), time.Now)
	enrollment := &fakeEnrollmentManager{issued: EnrollmentToken{
		ID:        "token-1",
		Token:     "shown-once",
		ExpiresAt: time.Date(2026, 8, 28, 11, 10, 0, 0, time.UTC),
	}}
	handler := Handler(registry, enrollment)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/servers/enrollment-tokens",
		strings.NewReader(`{"name":"执行节点-1","cloud_provider":"京东云","region":"华北","labels":{"用途":"批处理"}}`),
	)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "admin-1",
		Roles:  []auth.RoleName{auth.RoleOperator},
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("运维角色创建注册令牌应返回 403，实际为 %d，响应 %s", rec.Code, rec.Body.String())
	}
}

func TestDashboardCountsQueuedRuns(t *testing.T) {
	query := &fakeManagementQuery{dashboard: Dashboard{
		OnlineServers:    3,
		TotalServers:     4,
		RunningRuns:      6,
		QueuedRuns:       12,
		TodaySuccessRate: 98.4,
		Servers:          []ServerView{},
		RecentEvents:     []RecentEvent{},
	}}
	handler := ManagementHandler(query)
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "viewer-1",
		Roles:  []auth.RoleName{auth.RoleViewer},
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("运行总览应返回 200，实际为 %d，响应 %s", rec.Code, rec.Body.String())
	}
	var response Dashboard
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("解析运行总览响应：%v", err)
	}
	if response.QueuedRuns != 12 || response.RunningRuns != 6 || response.TodaySuccessRate != 98.4 {
		t.Fatalf("运行总览统计不完整：%+v", response)
	}
	if response.Servers == nil || response.RecentEvents == nil {
		t.Fatal("服务器和最近动态必须返回空数组而不是 null")
	}
}

func TestUpdateServerAllowsOperatorToRequestDrain(t *testing.T) {
	draining := true
	query := &fakeManagementQuery{updated: ServerView{ID: "server-1", Name: "执行节点-1", Draining: true}}
	handler := ManagementHandler(query)
	body := bytes.NewBufferString(`{"draining":true}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/servers/server-1", body)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{
		UserID: "operator-1",
		Roles:  []auth.RoleName{auth.RoleOperator},
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("运维人员请求排空应返回 200，实际为 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if query.updatedID != "server-1" || query.updatedInput.Draining == nil || *query.updatedInput.Draining != draining {
		t.Fatalf("排空更新未传给服务层：id=%s input=%+v", query.updatedID, query.updatedInput)
	}
}

type fakeEnrollmentManager struct {
	serverID string
	issued   EnrollmentToken
}

type fakeManagementQuery struct {
	dashboard    Dashboard
	servers      []ServerView
	updated      ServerView
	updatedID    string
	updatedInput UpdateServerInput
}

func (q *fakeManagementQuery) Dashboard(context.Context) (Dashboard, error) {
	return q.dashboard, nil
}

func (q *fakeManagementQuery) ListServers(context.Context) ([]ServerView, error) {
	return append([]ServerView(nil), q.servers...), nil
}

func (q *fakeManagementQuery) UpdateServer(_ context.Context, id string, input UpdateServerInput) (ServerView, error) {
	q.updatedID = id
	q.updatedInput = input
	return q.updated, nil
}

func (m *fakeEnrollmentManager) CreateToken(context.Context, EnrollmentTokenInput) (EnrollmentToken, error) {
	return m.issued, nil
}

func (m *fakeEnrollmentManager) Enroll(context.Context, string) (AgentCredentials, error) {
	return AgentCredentials{ServerID: m.serverID, Credential: "new-credential"}, nil
}

func (m *fakeEnrollmentManager) Authenticate(_ context.Context, credential string) (string, error) {
	if credential != "valid-agent-credential" {
		return "", ErrAgentCredentialInvalid
	}
	return m.serverID, nil
}
