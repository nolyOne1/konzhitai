package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"
)

func TestRotateCredentialPersistsHashAndReturnsPlaintextOnce(t *testing.T) {
	repository := &memoryCredentialRepository{}
	now := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)
	service := NewCredentialService(repository, &credentialDisconnector{}, func() time.Time { return now })

	credentials, err := service.Rotate(context.Background(), "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if credentials.ServerID != "server-1" || credentials.Credential == "" {
		t.Fatalf("轮换必须返回仅显示一次的新凭据：%+v", credentials)
	}
	expected := sha256.Sum256([]byte(credentials.Credential))
	if !bytes.Equal(repository.pending.CredentialHash, expected[:]) || bytes.Equal(repository.pending.CredentialHash, []byte(credentials.Credential)) {
		t.Fatal("凭据轮换只能持久化 SHA-256 哈希")
	}
	if repository.pending.IdentityID == "" || repository.pending.CreatedAt != now {
		t.Fatalf("待激活身份字段不完整：%+v", repository.pending)
	}
}

func TestEmergencyRevokeRevokesAllAndDisconnectsAgent(t *testing.T) {
	repository := &memoryCredentialRepository{}
	disconnector := &credentialDisconnector{}
	now := time.Date(2026, 8, 29, 11, 30, 0, 0, time.UTC)
	service := NewCredentialService(repository, disconnector, func() time.Time { return now })

	if err := service.Revoke(context.Background(), "server-1"); err != nil {
		t.Fatal(err)
	}
	if repository.revokedServerID != "server-1" || repository.revokedAt != now {
		t.Fatalf("紧急吊销应撤销服务器全部身份：server=%s at=%s", repository.revokedServerID, repository.revokedAt)
	}
	if disconnector.serverID != "server-1" {
		t.Fatalf("紧急吊销应立即断开代理：%s", disconnector.serverID)
	}
}

type memoryCredentialRepository struct {
	pending         PendingAgentIdentity
	revokedServerID string
	revokedAt       time.Time
}

func (r *memoryCredentialRepository) CreatePendingIdentity(_ context.Context, identity PendingAgentIdentity) error {
	r.pending = identity
	return nil
}

func (r *memoryCredentialRepository) RevokeServerCredentials(_ context.Context, serverID string, revokedAt time.Time) error {
	r.revokedServerID = serverID
	r.revokedAt = revokedAt
	return nil
}

type credentialDisconnector struct{ serverID string }

func (d *credentialDisconnector) Disconnect(serverID string) { d.serverID = serverID }
