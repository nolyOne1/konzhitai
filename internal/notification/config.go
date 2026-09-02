package notification

import (
	"context"
	"log"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"yunling.local/platform/internal/secret"
)

var feishuWebhookPath = regexp.MustCompile(`^/open-apis/bot/v2/hook/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

type ConfigRepository interface {
	Get(context.Context) (ConfigRecord, bool, error)
	Save(context.Context, ConfigRecord, string, string, string) (ConfigRecord, error)
	DeleteUnreferencedSystemSecrets(context.Context) error
}

type SystemSecretCreator interface {
	CreateSystem(context.Context, string, []byte) (secret.Metadata, error)
}

type ConfigService struct {
	repository ConfigRepository
	secrets    SystemSecretCreator
}

func NewConfigService(repository ConfigRepository, secrets SystemSecretCreator) *ConfigService {
	return &ConfigService{repository: repository, secrets: secrets}
}

func ValidateFeishuWebhook(value string) error {
	if strings.TrimSpace(value) != value || value == "" {
		return ErrInvalidWebhook
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "open.feishu.cn" ||
		parsed.Hostname() != "open.feishu.cn" || parsed.Port() != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return ErrInvalidWebhook
	}
	matches := feishuWebhookPath.FindStringSubmatch(parsed.Path)
	if len(matches) != 2 {
		return ErrInvalidWebhook
	}
	if _, err := uuid.Parse(matches[1]); err != nil {
		return ErrInvalidWebhook
	}
	return nil
}

func (s *ConfigService) Get(ctx context.Context) (FeishuConfigView, error) {
	if s == nil || s.repository == nil || s.secrets == nil {
		return FeishuConfigView{}, ErrUnavailable
	}
	record, exists, err := s.repository.Get(ctx)
	if err != nil {
		return FeishuConfigView{}, err
	}
	if !exists {
		return FeishuConfigView{}, nil
	}
	return configView(record), nil
}

func (s *ConfigService) Update(ctx context.Context, actorID, ipAddress string, input FeishuConfigInput) (FeishuConfigView, error) {
	if s == nil || s.repository == nil || s.secrets == nil || strings.TrimSpace(actorID) == "" {
		return FeishuConfigView{}, ErrUnavailable
	}
	existing, exists, err := s.repository.Get(ctx)
	if err != nil {
		return FeishuConfigView{}, err
	}
	hasWebhook := input.Webhook != ""
	hasSigningSecret := input.SigningSecret != ""
	if hasWebhook != hasSigningSecret || (!hasWebhook && !exists) {
		return FeishuConfigView{}, ErrInvalidConfig
	}

	record := existing
	record.Enabled = input.Enabled
	createdSecrets := false
	if hasWebhook {
		if ValidateFeishuWebhook(input.Webhook) != nil || strings.TrimSpace(input.SigningSecret) == "" {
			return FeishuConfigView{}, ErrInvalidConfig
		}
		webhookBytes := []byte(input.Webhook)
		defer clear(webhookBytes)
		webhookSecret, err := s.secrets.CreateSystem(
			secret.WithCreator(ctx, actorID),
			"notification/feishu/webhook/"+uuid.NewString(),
			webhookBytes,
		)
		if err != nil {
			return FeishuConfigView{}, err
		}
		createdSecrets = true
		defer s.cleanupUnreferenced(ctx)

		signingBytes := []byte(input.SigningSecret)
		defer clear(signingBytes)
		signingSecret, err := s.secrets.CreateSystem(
			secret.WithCreator(ctx, actorID),
			"notification/feishu/signing/"+uuid.NewString(),
			signingBytes,
		)
		if err != nil {
			return FeishuConfigView{}, err
		}
		record.WebhookSecretID = webhookSecret.ID
		record.SigningSecretID = signingSecret.ID
		record.MaskedDestination = maskWebhook(input.Webhook)
	}

	action := "operations.feishu.update"
	if !input.Enabled {
		action = "operations.feishu.disable"
	}
	record, err = s.repository.Save(ctx, record, actorID, ipAddress, action)
	if err != nil {
		return FeishuConfigView{}, err
	}
	if !createdSecrets {
		// A previous interrupted update may have left an unreferenced internal secret.
		s.cleanupUnreferenced(ctx)
	}
	return configView(record), nil
}

func (s *ConfigService) cleanupUnreferenced(ctx context.Context) {
	if err := s.repository.DeleteUnreferencedSystemSecrets(ctx); err != nil {
		log.Printf("清理未引用的飞书系统秘密失败")
	}
}

func configView(record ConfigRecord) FeishuConfigView {
	return FeishuConfigView{
		Configured:        record.WebhookSecretID != "" && record.SigningSecretID != "",
		Enabled:           record.Enabled,
		MaskedDestination: record.MaskedDestination,
		UpdatedAt:         record.UpdatedAt,
	}
}

func maskWebhook(webhook string) string {
	matches := feishuWebhookPath.FindStringSubmatch(webhookURLPath(webhook))
	if len(matches) != 2 || len(matches[1]) < 4 {
		return "飞书机器人"
	}
	return "飞书机器人 …" + matches[1][len(matches[1])-4:]
}

func webhookURLPath(webhook string) string {
	parsed, err := url.Parse(webhook)
	if err != nil {
		return ""
	}
	return parsed.Path
}
