package server

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrInvalidCredentialOperation = errors.New("代理凭据操作无效")

type PendingAgentIdentity struct {
	IdentityID     string
	ServerID       string
	CredentialHash []byte
	CreatedAt      time.Time
}

type CredentialRepository interface {
	CreatePendingIdentity(context.Context, PendingAgentIdentity) error
	RevokeServerCredentials(context.Context, string, time.Time) error
}

type CredentialService struct {
	repository   CredentialRepository
	disconnector AgentDisconnector
	now          func() time.Time
}

func NewCredentialService(repository CredentialRepository, disconnector AgentDisconnector, now func() time.Time) *CredentialService {
	if disconnector == nil {
		disconnector = noopAgentDisconnector{}
	}
	if now == nil {
		now = time.Now
	}
	return &CredentialService{repository: repository, disconnector: disconnector, now: now}
}

func (s *CredentialService) Rotate(ctx context.Context, serverID string) (AgentCredentials, error) {
	serverID = strings.TrimSpace(serverID)
	if s == nil || s.repository == nil || serverID == "" {
		return AgentCredentials{}, ErrInvalidCredentialOperation
	}
	identityID, err := secureUUID()
	if err != nil {
		return AgentCredentials{}, err
	}
	credential, err := secureToken(32)
	if err != nil {
		return AgentCredentials{}, err
	}
	if err := s.repository.CreatePendingIdentity(ctx, PendingAgentIdentity{
		IdentityID: identityID, ServerID: serverID, CredentialHash: hashSecret(credential),
		CreatedAt: s.now().UTC(),
	}); err != nil {
		return AgentCredentials{}, err
	}
	return AgentCredentials{ServerID: serverID, Credential: credential}, nil
}

func (s *CredentialService) Revoke(ctx context.Context, serverID string) error {
	serverID = strings.TrimSpace(serverID)
	if s == nil || s.repository == nil || serverID == "" {
		return ErrInvalidCredentialOperation
	}
	if err := s.repository.RevokeServerCredentials(ctx, serverID, s.now().UTC()); err != nil {
		return err
	}
	s.disconnector.Disconnect(serverID)
	return nil
}
