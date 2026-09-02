package notification_test

import (
	"context"
	"errors"
	"testing"

	"yunling.local/platform/internal/notification"
	"yunling.local/platform/internal/secret"
)

const validWebhook = "https://open.feishu.cn/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef"

func TestValidateFeishuWebhookAcceptsOnlyOfficialV2URL(t *testing.T) {
	if err := notification.ValidateFeishuWebhook(validWebhook); err != nil {
		t.Fatalf("标准飞书 V2 Webhook 应有效：%v", err)
	}
	invalid := []string{
		"http://open.feishu.cn/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef",
		"https://user@open.feishu.cn/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef",
		"https://open.feishu.cn:443/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef",
		validWebhook + "?debug=true",
		validWebhook + "#fragment",
		"https://127.0.0.1/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef",
		"https://open.feishu.cn.evil.example/open-apis/bot/v2/hook/01234567-89ab-cdef-0123-456789abcdef",
		"https://open.feishu.cn/open-apis/bot/v2/hook%2f01234567-89ab-cdef-0123-456789abcdef",
		"https://open.feishu.cn/open-apis/bot/v1/hook/01234567-89ab-cdef-0123-456789abcdef",
	}
	for _, value := range invalid {
		if notification.ValidateFeishuWebhook(value) == nil {
			t.Errorf("必须拒绝 Webhook %q", value)
		}
	}
}

func TestConfigServiceRequiresCredentialPairsAndReusesExistingOnToggle(t *testing.T) {
	repository := &configRepository{}
	secrets := &systemSecretCreator{}
	service := notification.NewConfigService(repository, secrets)
	ctx := context.Background()

	if _, err := service.Update(ctx, "actor-1", "127.0.0.1", notification.FeishuConfigInput{Enabled: true}); !errors.Is(err, notification.ErrInvalidConfig) {
		t.Fatalf("未配置时不得空值启用，实际错误：%v", err)
	}
	if _, err := service.Update(ctx, "actor-1", "127.0.0.1", notification.FeishuConfigInput{Enabled: true, Webhook: validWebhook}); !errors.Is(err, notification.ErrInvalidConfig) {
		t.Fatalf("只提交 Webhook 必须拒绝，实际错误：%v", err)
	}

	view, err := service.Update(ctx, "actor-1", "127.0.0.1", notification.FeishuConfigInput{
		Enabled: true, Webhook: validWebhook, SigningSecret: "signing-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Configured || !view.Enabled || view.MaskedDestination != "飞书机器人 …cdef" {
		t.Fatalf("配置视图未脱敏：%+v", view)
	}
	if len(secrets.values) != 2 || repository.saved.WebhookSecretID == "" || repository.saved.SigningSecretID == "" {
		t.Fatalf("必须保存两个系统秘密引用：secrets=%d saved=%+v", len(secrets.values), repository.saved)
	}

	previousWebhookID := repository.saved.WebhookSecretID
	previousSigningID := repository.saved.SigningSecretID
	view, err = service.Update(ctx, "actor-1", "127.0.0.1", notification.FeishuConfigInput{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if view.Enabled || repository.saved.WebhookSecretID != previousWebhookID || repository.saved.SigningSecretID != previousSigningID {
		t.Fatalf("停用时必须保留秘密引用：%+v", repository.saved)
	}
	if repository.action != "operations.feishu.disable" {
		t.Fatalf("停用审计动作错误：%q", repository.action)
	}
}

type configRepository struct {
	saved  notification.ConfigRecord
	exists bool
	action string
}

func (r *configRepository) Get(context.Context) (notification.ConfigRecord, bool, error) {
	return r.saved, r.exists, nil
}

func (r *configRepository) Save(_ context.Context, record notification.ConfigRecord, _, _, action string) (notification.ConfigRecord, error) {
	r.saved, r.exists, r.action = record, true, action
	return record, nil
}

func (r *configRepository) DeleteUnreferencedSystemSecrets(context.Context) error { return nil }

type systemSecretCreator struct {
	values [][]byte
}

func (s *systemSecretCreator) CreateSystem(_ context.Context, name string, plaintext []byte) (secret.Metadata, error) {
	s.values = append(s.values, append([]byte(nil), plaintext...))
	return secret.Metadata{ID: secret.ID(name + "-id"), Name: name, Scope: secret.ScopeSystem}, nil
}
