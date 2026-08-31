package notification

import (
	"errors"
	"time"

	"yunling.local/platform/internal/secret"
)

var (
	ErrInvalidWebhook = errors.New("飞书 Webhook 地址无效")
	ErrInvalidConfig  = errors.New("飞书通知配置无效")
	ErrUnavailable    = errors.New("飞书通知服务尚未配置")
)

type FeishuConfigInput struct {
	Enabled       bool   `json:"enabled"`
	Webhook       string `json:"webhook"`
	SigningSecret string `json:"signingSecret"`
}

type FeishuConfigView struct {
	Configured        bool      `json:"configured"`
	Enabled           bool      `json:"enabled"`
	MaskedDestination string    `json:"maskedDestination"`
	UpdatedAt         time.Time `json:"updatedAt,omitempty"`
}

type ConfigRecord struct {
	Enabled           bool
	WebhookSecretID   secret.ID
	SigningSecretID   secret.ID
	MaskedDestination string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
