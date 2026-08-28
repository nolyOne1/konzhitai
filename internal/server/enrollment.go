package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

var (
	ErrEnrollmentTokenInvalid = errors.New("注册令牌无效、已使用或已过期")
	ErrAgentCredentialInvalid = errors.New("代理凭据无效或已撤销")
)

type EnrollmentTokenInput struct {
	Name          string
	CloudProvider string
	Region        string
	Labels        map[string]string
}

type EnrollmentToken struct {
	ID        string
	Token     string
	ExpiresAt time.Time
}

type EnrollmentTokenRecord struct {
	ID            string
	TokenHash     []byte
	Name          string
	CloudProvider string
	Region        string
	Labels        map[string]string
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

type EnrollmentClaim struct {
	TokenHash      []byte
	ServerID       string
	IdentityID     string
	CredentialHash []byte
	UsedAt         time.Time
}

type AgentCredentials struct {
	ServerID   string `json:"server_id"`
	Credential string `json:"credential"`
}

type EnrollmentRepository interface {
	CreateEnrollmentToken(ctx context.Context, token EnrollmentTokenRecord) error
	ConsumeEnrollmentToken(ctx context.Context, claim EnrollmentClaim) (bool, error)
	FindServerByCredentialHash(ctx context.Context, credentialHash []byte, authenticatedAt time.Time) (string, error)
}

type EnrollmentService struct {
	repository EnrollmentRepository
	clock      Clock
	tokenTTL   time.Duration
}

func NewEnrollmentService(repository EnrollmentRepository, clock Clock) *EnrollmentService {
	return &EnrollmentService{
		repository: repository,
		clock:      clock,
		tokenTTL:   10 * time.Minute,
	}
}

func (s *EnrollmentService) CreateToken(ctx context.Context, input EnrollmentTokenInput) (EnrollmentToken, error) {
	token, err := secureToken(32)
	if err != nil {
		return EnrollmentToken{}, err
	}
	id, err := secureUUID()
	if err != nil {
		return EnrollmentToken{}, err
	}
	now := s.clock().UTC()
	expiresAt := now.Add(s.tokenTTL)
	record := EnrollmentTokenRecord{
		ID:            id,
		TokenHash:     hashSecret(token),
		Name:          input.Name,
		CloudProvider: input.CloudProvider,
		Region:        input.Region,
		Labels:        copyLabels(input.Labels),
		ExpiresAt:     expiresAt,
		CreatedAt:     now,
	}
	if err := s.repository.CreateEnrollmentToken(ctx, record); err != nil {
		return EnrollmentToken{}, fmt.Errorf("保存注册令牌：%w", err)
	}
	return EnrollmentToken{ID: id, Token: token, ExpiresAt: expiresAt}, nil
}

func (s *EnrollmentService) Enroll(ctx context.Context, token string) (AgentCredentials, error) {
	serverID, err := secureUUID()
	if err != nil {
		return AgentCredentials{}, err
	}
	identityID, err := secureUUID()
	if err != nil {
		return AgentCredentials{}, err
	}
	credential, err := secureToken(32)
	if err != nil {
		return AgentCredentials{}, err
	}
	accepted, err := s.repository.ConsumeEnrollmentToken(ctx, EnrollmentClaim{
		TokenHash:      hashSecret(token),
		ServerID:       serverID,
		IdentityID:     identityID,
		CredentialHash: hashSecret(credential),
		UsedAt:         s.clock().UTC(),
	})
	if err != nil {
		return AgentCredentials{}, fmt.Errorf("注册代理：%w", err)
	}
	if !accepted {
		return AgentCredentials{}, ErrEnrollmentTokenInvalid
	}
	return AgentCredentials{ServerID: serverID, Credential: credential}, nil
}

func (s *EnrollmentService) Authenticate(ctx context.Context, credential string) (string, error) {
	if credential == "" {
		return "", ErrAgentCredentialInvalid
	}
	serverID, err := s.repository.FindServerByCredentialHash(ctx, hashSecret(credential), s.clock().UTC())
	if err != nil {
		return "", ErrAgentCredentialInvalid
	}
	return serverID, nil
}

func hashSecret(value string) []byte {
	hash := sha256.Sum256([]byte(value))
	return hash[:]
}

func secureToken(length int) (string, error) {
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成安全令牌：%w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func secureUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成唯一编号：%w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func copyLabels(labels map[string]string) map[string]string {
	copy := make(map[string]string, len(labels))
	for key, value := range labels {
		copy[key] = value
	}
	return copy
}
