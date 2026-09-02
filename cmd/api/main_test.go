package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/server"
)

func TestLoadAgentReleaseHandlerFailsClosed(t *testing.T) {
	handler := loadAgentReleaseHandler(t.TempDir())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/releases/agent/latest", nil))
	if response.Code != http.StatusServiceUnavailable || strings.TrimSpace(response.Body.String()) != "代理发布暂不可用" {
		t.Fatalf("发布目录损坏时必须失败关闭：status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestNewHTTPServerUsesBoundedTimeouts(t *testing.T) {
	handler := http.NewServeMux()
	server := newHTTPServer(":9090", handler)
	if server.Addr != ":9090" || server.Handler != handler {
		t.Fatalf("HTTP Server 装配错误：%+v", server)
	}
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 30*time.Second ||
		server.WriteTimeout != 5*time.Minute || server.IdleTimeout != 60*time.Second {
		t.Fatalf("HTTP 超时边界错误：header=%s read=%s write=%s idle=%s",
			server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
}

func TestOfflinePublisherOnlyReconcilesRuns(t *testing.T) {
	reconciler := &recordingOfflineReconciler{}
	publisher := offlineRunPublisher{reconciler: reconciler}
	if err := publisher.Publish(context.Background(), server.Event{Type: "server.offline", ServerID: "server-1"}); err != nil {
		t.Fatal(err)
	}
	if reconciler.serverID != "server-1" {
		t.Fatalf("离线运行对账未执行：%q", reconciler.serverID)
	}
	if err := publisher.Publish(context.Background(), server.Event{Type: "server.online", ServerID: "server-2"}); err != nil {
		t.Fatal(err)
	}
	if reconciler.calls != 1 {
		t.Fatalf("非离线事件不得触发对账：%d", reconciler.calls)
	}
}

type recordingOfflineReconciler struct {
	serverID string
	calls    int
}

func (r *recordingOfflineReconciler) ServerOffline(_ context.Context, serverID string) error {
	r.serverID = serverID
	r.calls++
	return nil
}
