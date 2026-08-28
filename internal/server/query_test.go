package server

import (
	"context"
	"testing"
)

func TestDisableServerDisconnectsCurrentAgent(t *testing.T) {
	enabled := false
	repository := &fakeManagementRepository{updated: ServerView{
		ID:      "server-1",
		Name:    "执行节点-1",
		Enabled: false,
	}}
	disconnector := &recordingDisconnector{}
	service := NewManagementService(repository, disconnector)

	_, err := service.UpdateServer(context.Background(), "server-1", UpdateServerInput{Enabled: &enabled})
	if err != nil {
		t.Fatalf("停用服务器：%v", err)
	}
	if disconnector.serverID != "server-1" {
		t.Fatalf("停用服务器必须断开当前代理会话，实际断开 %q", disconnector.serverID)
	}
}

type fakeManagementRepository struct {
	updated ServerView
}

func (r *fakeManagementRepository) Dashboard(context.Context) (Dashboard, error) {
	return Dashboard{}, nil
}

func (r *fakeManagementRepository) ListServers(context.Context) ([]ServerView, error) {
	return nil, nil
}

func (r *fakeManagementRepository) UpdateServer(context.Context, string, UpdateServerInput) (ServerView, error) {
	return r.updated, nil
}

type recordingDisconnector struct {
	serverID string
}

func (d *recordingDisconnector) Disconnect(serverID string) {
	d.serverID = serverID
}
