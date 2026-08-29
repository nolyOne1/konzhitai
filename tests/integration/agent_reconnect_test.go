package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/server"
	"yunling.local/platform/internal/task"
)

func TestAgentReconnectReconcilesMatchingProcessThroughWebSocket(t *testing.T) {
	db := recoveryDatabase(t)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	ids := seedRecoveryFixture(t, db, now)
	ctx := context.Background()
	const executionToken = "reconnect-token"
	if _, err := db.Exec(ctx, `UPDATE task_runs SET execution_token=$2 WHERE id=$1`, ids.runningRunID, executionToken); err != nil {
		t.Fatalf("设置执行令牌：%v", err)
	}
	reconciler := task.NewReconciler(task.NewPostgresReconcileStore(db), func() time.Time { return now })
	if err := reconciler.ServerOffline(ctx, ids.lease.ServerID); err != nil {
		t.Fatalf("模拟服务器失联：%v", err)
	}

	repository := server.NewPostgresRepository(db)
	handler := server.Handler(
		server.NewRegistry(repository, func() time.Time { return now }),
		&integrationEnrollment{serverID: ids.lease.ServerID},
		server.WithRunningReconciler(reconciler),
	)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	header := http.Header{}
	header.Set("Authorization", "Bearer integration-agent-credential")
	connectionCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(
		connectionCtx,
		"ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/agent/connect",
		&websocket.DialOptions{HTTPHeader: header},
	)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("代理重连 WebSocket：status=%d err=%v", status, err)
	}
	defer connection.CloseNow()
	if err := wsjson.Write(connectionCtx, connection, agentprotocol.RunningReport{
		MessageType: "running_report",
		ServerID:    "forged-server-id",
		ReportedAt:  now,
		Processes: []agentprotocol.RunningProcess{{
			RunID: ids.runningRunID, ExecutionToken: executionToken,
		}},
	}); err != nil {
		t.Fatalf("发送重连运行清单：%v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		var state task.RunState
		var processConfirmedGone bool
		if err := db.QueryRow(ctx, `SELECT state, process_confirmed_gone FROM task_runs WHERE id=$1`, ids.runningRunID).Scan(&state, &processConfirmedGone); err != nil {
			t.Fatalf("读取重连对账状态：%v", err)
		}
		if state == task.Running {
			if processConfirmedGone {
				t.Fatal("匹配执行令牌的进程不得被标记为已消失")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("代理重连后任务未恢复运行，实际=%s", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var reconciledEvents int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM run_events WHERE task_run_id=$1 AND event_type='run.reconciled'`, ids.runningRunID).Scan(&reconciledEvents); err != nil {
		t.Fatalf("读取重连对账事件：%v", err)
	}
	if reconciledEvents != 1 {
		t.Fatalf("重连成功必须留下一个对账事件，实际=%d", reconciledEvents)
	}
}

type integrationEnrollment struct{ serverID string }

func (e *integrationEnrollment) CreateToken(context.Context, server.EnrollmentTokenInput) (server.EnrollmentToken, error) {
	return server.EnrollmentToken{}, nil
}

func (e *integrationEnrollment) Enroll(context.Context, string) (server.AgentCredentials, error) {
	return server.AgentCredentials{ServerID: e.serverID, Credential: "integration-agent-credential"}, nil
}

func (e *integrationEnrollment) Authenticate(_ context.Context, credential string) (string, error) {
	if credential != "integration-agent-credential" {
		return "", server.ErrAgentCredentialInvalid
	}
	return e.serverID, nil
}
