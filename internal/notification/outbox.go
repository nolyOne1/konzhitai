package notification

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"yunling.local/platform/internal/secret"
)

type DeliveryStatus string

const (
	DeliveryPending  DeliveryStatus = "pending"
	DeliverySending  DeliveryStatus = "sending"
	DeliveryRetrying DeliveryStatus = "retrying"
	DeliverySent     DeliveryStatus = "sent"
	DeliveryFailed   DeliveryStatus = "failed"
)

type Delivery struct {
	ID            string         `json:"id"`
	EventType     string         `json:"eventType"`
	Status        DeliveryStatus `json:"status"`
	Attempts      int            `json:"attempts"`
	NextAttemptAt time.Time      `json:"nextAttemptAt,omitempty"`
	LeaseUntil    *time.Time     `json:"leaseUntil,omitempty"`
	LastError     string         `json:"lastError,omitempty"`
	ResponseID    string         `json:"-"`
	CreatedAt     time.Time      `json:"createdAt"`
	SentAt        *time.Time     `json:"sentAt,omitempty"`
	UpdatedAt     time.Time      `json:"updatedAt"`
}

type ClaimedDelivery struct {
	Delivery
	Payload         FrozenMessage
	WebhookSecretID secret.ID
	SigningSecretID secret.ID
}

type OutboxRepository interface {
	EnqueueTest(context.Context, string, FrozenMessage, string, time.Time) (Delivery, error)
	ClaimDue(context.Context, time.Time, time.Duration) (ClaimedDelivery, bool, error)
	MarkSent(context.Context, string, string, time.Time) error
	MarkFailed(context.Context, string, string, time.Time, bool, time.Time) error
	GetDelivery(context.Context, string) (Delivery, error)
}

type SecretResolver interface {
	Resolve(context.Context, []secret.ID) (map[secret.ID][]byte, error)
}

type MessageSender interface {
	Send(context.Context, string, string, FrozenMessage) (string, error)
}

type OutboxService struct {
	repository OutboxRepository
	secrets    SecretResolver
	sender     MessageSender
	now        func() time.Time
}

func NewOutboxService(repository OutboxRepository, secrets SecretResolver, sender MessageSender, now func() time.Time) *OutboxService {
	if now == nil {
		now = time.Now
	}
	return &OutboxService{repository: repository, secrets: secrets, sender: sender, now: now}
}

func (s *OutboxService) EnqueueTest(ctx context.Context, actorID string) (Delivery, error) {
	if s == nil || s.repository == nil || strings.TrimSpace(actorID) == "" {
		return Delivery{}, ErrUnavailable
	}
	now := s.now().UTC()
	return s.repository.EnqueueTest(ctx, actorID, FrozenMessage{
		Code: "notification_test", Severity: "info", Title: "云令飞书测试消息",
		SourceType: "system", SourceID: "yunling", OccurrenceCount: 1, OccurredAt: now,
	}, "notification:test:"+uuid.NewString(), now)
}

func (s *OutboxService) GetDelivery(ctx context.Context, id string) (Delivery, error) {
	if s == nil || s.repository == nil || strings.TrimSpace(id) == "" {
		return Delivery{}, ErrDeliveryNotFound
	}
	return s.repository.GetDelivery(ctx, id)
}

func (s *OutboxService) DeliverDue(ctx context.Context) error {
	if s == nil || s.repository == nil || s.secrets == nil || s.sender == nil {
		return ErrUnavailable
	}
	for range 100 {
		now := s.now().UTC()
		claim, ok, err := s.repository.ClaimDue(ctx, now, 30*time.Second)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := s.deliver(ctx, claim, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *OutboxService) deliver(ctx context.Context, claim ClaimedDelivery, now time.Time) error {
	values, err := s.secrets.Resolve(ctx, []secret.ID{claim.WebhookSecretID, claim.SigningSecretID})
	if err != nil || len(values) != 2 {
		clearResolved(values)
		return s.recordFailure(ctx, claim, "读取飞书通知凭据失败", now)
	}
	webhook := values[claim.WebhookSecretID]
	signing := values[claim.SigningSecretID]
	responseID, sendErr := s.sender.Send(ctx, string(webhook), string(signing), claim.Payload)
	clearResolved(values)
	if sendErr != nil {
		return s.recordFailure(ctx, claim, "飞书消息发送失败", now)
	}
	return s.repository.MarkSent(ctx, claim.ID, responseID, now)
}

func (s *OutboxService) recordFailure(ctx context.Context, claim ClaimedDelivery, message string, now time.Time) error {
	terminal := claim.Attempts >= 24
	next := now
	if !terminal {
		next = now.Add(retryDelay(claim.Attempts))
	}
	return s.repository.MarkFailed(ctx, claim.ID, message, next, terminal, now)
}

func clearResolved(values map[secret.ID][]byte) {
	for _, value := range values {
		clear(value)
	}
}

func retryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 15 * time.Minute
	case 4:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}
