package main

import (
	"context"
	"testing"

	"yunling.local/platform/internal/server"
)

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
