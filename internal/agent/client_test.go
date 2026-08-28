package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"yunling.local/platform/internal/agentprotocol"
)

func TestClientSendsMonotonicHeartbeatEveryFiveSeconds(t *testing.T) {
	ticker := &fakeTicker{
		values: make(chan time.Time, 2),
		ready:  make(chan time.Duration, 1),
	}
	sender := &recordingHeartbeatSender{heartbeats: make(chan agentprotocol.Heartbeat, 2)}
	collector := NewCollector(fakeStats{snapshot: Stats{
		CPUTotalMilli:    4000,
		CPUUsedMilli:     1000,
		MemoryTotalBytes: 8 << 30,
		MemoryUsedBytes:  2 << 30,
		DiskTotalBytes:   80 << 30,
		DiskFreeBytes:    40 << 30,
	}}, []string{"bash"})
	client := NewClient(
		"server-1",
		"0.1.0",
		collector,
		sender,
		WithTickerFactory(func(interval time.Duration) Ticker {
			ticker.ready <- interval
			return ticker
		}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	if interval := <-ticker.ready; interval != 5*time.Second {
		t.Fatalf("心跳间隔必须为 5 秒，实际为 %s", interval)
	}
	firstAt := time.Date(2026, 8, 28, 11, 0, 5, 0, time.UTC)
	secondAt := firstAt.Add(5 * time.Second)
	ticker.values <- firstAt
	first := receiveHeartbeat(t, sender.heartbeats)
	ticker.values <- secondAt
	second := receiveHeartbeat(t, sender.heartbeats)

	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("心跳序号必须从 1 单调递增，实际为 %d、%d", first.Sequence, second.Sequence)
	}
	if first.ServerID != "server-1" || first.AgentVersion != "0.1.0" {
		t.Fatalf("心跳必须携带代理身份和版本：%+v", first)
	}
	if !first.SentAt.Equal(firstAt) || !second.SentAt.Equal(secondAt) {
		t.Fatalf("心跳发送时间必须来自触发时刻：%s、%s", first.SentAt, second.SentAt)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消客户端后应正常退出，实际为 %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消客户端后未在 1 秒内退出")
	}
}

func TestWebSocketSenderUsesAgentCredentialAndWritesHeartbeat(t *testing.T) {
	received := make(chan agentprotocol.Heartbeat, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-secret" {
			http.Error(w, "代理凭据错误", http.StatusUnauthorized)
			return
		}
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		var heartbeat agentprotocol.Heartbeat
		if err := wsjson.Read(context.Background(), connection, &heartbeat); err == nil {
			received <- heartbeat
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	sender, err := DialHeartbeatSender(ctx, server.URL, "agent-secret")
	if err != nil {
		t.Fatalf("连接中央 WebSocket：%v", err)
	}
	defer sender.Close()
	want := agentprotocol.Heartbeat{ServerID: "server-1", Sequence: 7, CPUUsedMilli: 2100}
	if err := sender.SendHeartbeat(ctx, want); err != nil {
		t.Fatalf("发送 WebSocket 心跳：%v", err)
	}
	select {
	case got := <-received:
		if got.ServerID != want.ServerID || got.Sequence != 7 || got.CPUUsedMilli != 2100 {
			t.Fatalf("中央服务收到的心跳不完整：%+v", got)
		}
	case <-ctx.Done():
		t.Fatal("中央服务未在 3 秒内收到 WebSocket 心跳")
	}
}

func receiveHeartbeat(t *testing.T, heartbeats <-chan agentprotocol.Heartbeat) agentprotocol.Heartbeat {
	t.Helper()
	select {
	case heartbeat := <-heartbeats:
		return heartbeat
	case <-time.After(time.Second):
		t.Fatal("1 秒内未收到心跳")
		return agentprotocol.Heartbeat{}
	}
}

type fakeTicker struct {
	values chan time.Time
	ready  chan time.Duration
}

func (t *fakeTicker) C() <-chan time.Time { return t.values }
func (t *fakeTicker) Stop()               {}

type recordingHeartbeatSender struct {
	heartbeats chan agentprotocol.Heartbeat
}

func (s *recordingHeartbeatSender) SendHeartbeat(_ context.Context, heartbeat agentprotocol.Heartbeat) error {
	s.heartbeats <- heartbeat
	return nil
}
