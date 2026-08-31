package notification

import (
	"context"
	"errors"
	"testing"
	"time"

	"yunling.local/platform/internal/secret"
)

const outboxTestWebhook = "https://open.feishu.cn/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef"

func TestRetryDelayUsesFixedBackoffTiers(t *testing.T) {
	wants := map[int]time.Duration{
		1: time.Minute, 2: 5 * time.Minute, 3: 15 * time.Minute,
		4: time.Hour, 5: 6 * time.Hour, 23: 6 * time.Hour,
	}
	for attempt, want := range wants {
		if got := retryDelay(attempt); got != want {
			t.Errorf("attempt=%d got=%s want=%s", attempt, got, want)
		}
	}
}

func TestOutboxServiceMarksSuccessRetryAndTerminalFailure(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		attempts   int
		sendError  error
		wantStatus DeliveryStatus
		wantNext   time.Time
	}{
		{name: "success", attempts: 1, wantStatus: DeliverySent},
		{name: "retry", attempts: 2, sendError: errors.New("remote unavailable"), wantStatus: DeliveryRetrying, wantNext: now.Add(5 * time.Minute)},
		{name: "terminal", attempts: 24, sendError: errors.New("remote unavailable"), wantStatus: DeliveryFailed, wantNext: now},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &memoryOutboxRepository{claim: ClaimedDelivery{
				Delivery:        Delivery{ID: "delivery-1", Status: DeliverySending, Attempts: test.attempts},
				Payload:         FrozenMessage{Title: "测试消息"},
				WebhookSecretID: "webhook-id", SigningSecretID: "signing-id",
			}}
			resolver := &memoryResolver{values: map[secret.ID][]byte{
				"webhook-id": []byte(outboxTestWebhook), "signing-id": []byte("signing-secret"),
			}}
			sender := &memorySender{err: test.sendError}
			service := NewOutboxService(repository, resolver, sender, func() time.Time { return now })

			if err := service.DeliverDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			if repository.markedStatus != test.wantStatus || !repository.markedNext.Equal(test.wantNext) {
				t.Fatalf("状态转换错误：status=%s next=%s", repository.markedStatus, repository.markedNext)
			}
			for _, value := range resolver.values {
				for _, item := range value {
					if item != 0 {
						t.Fatal("发件箱发送后必须清理明文秘密")
					}
				}
			}
		})
	}
}

type memoryOutboxRepository struct {
	claim        ClaimedDelivery
	claimed      bool
	markedStatus DeliveryStatus
	markedNext   time.Time
}

func (r *memoryOutboxRepository) EnqueueTest(context.Context, string, FrozenMessage, string, time.Time) (Delivery, error) {
	return Delivery{}, nil
}

func (r *memoryOutboxRepository) ClaimDue(context.Context, time.Time, time.Duration) (ClaimedDelivery, bool, error) {
	if r.claimed {
		return ClaimedDelivery{}, false, nil
	}
	r.claimed = true
	return r.claim, true, nil
}

func (r *memoryOutboxRepository) MarkSent(_ context.Context, _ string, _ string, _ time.Time) error {
	r.markedStatus, r.markedNext = DeliverySent, time.Time{}
	return nil
}

func (r *memoryOutboxRepository) MarkFailed(_ context.Context, _ string, _ string, next time.Time, terminal bool, _ time.Time) error {
	r.markedStatus, r.markedNext = DeliveryRetrying, next
	if terminal {
		r.markedStatus = DeliveryFailed
	}
	return nil
}

func (r *memoryOutboxRepository) GetDelivery(context.Context, string) (Delivery, error) {
	return Delivery{}, nil
}

type memoryResolver struct{ values map[secret.ID][]byte }

func (r *memoryResolver) Resolve(context.Context, []secret.ID) (map[secret.ID][]byte, error) {
	return r.values, nil
}

type memorySender struct{ err error }

func (s *memorySender) Send(context.Context, string, string, FrozenMessage) (string, error) {
	return "message-1", s.err
}
