package secret_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"yunling.local/platform/internal/secret"
)

func TestCreateUsesEnvelopeEncryptionAndMetadataNeverEchoesSecret(t *testing.T) {
	repository := &memoryRepository{items: map[secret.ID]secret.StoredSecret{}}
	service := secret.NewService(repository, fixedKeyProvider())
	plaintext := []byte("数据库口令-very-secret")

	metadata, err := service.Create(context.Background(), "生产数据库密码", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	stored := repository.items[metadata.ID]
	if bytes.Contains(stored.Ciphertext, plaintext) || bytes.Contains(stored.EncryptedDataKey, plaintext) {
		t.Fatal("数据库不得保存密钥明文")
	}
	if len(stored.CiphertextNonce) != 12 || len(stored.DataKeyNonce) != 12 || len(stored.EncryptedDataKey) == 0 {
		t.Fatalf("信封加密字段不完整：%+v", stored)
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, plaintext) || bytes.Contains(body, stored.Ciphertext) || bytes.Contains(body, stored.EncryptedDataKey) {
		t.Fatalf("元数据接口不得回显密文材料：%s", body)
	}
}

func TestResolveForRunDecryptsReferencedSecrets(t *testing.T) {
	repository := &memoryRepository{items: map[secret.ID]secret.StoredSecret{}}
	service := secret.NewService(repository, fixedKeyProvider())
	first, err := service.Create(context.Background(), "数据库密码", []byte("first-secret"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), "签名密钥", []byte("second-secret"))
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := service.ResolveForRun(context.Background(), []secret.ID{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resolved[string(first.ID)] != "first-secret" || resolved[string(second.ID)] != "second-secret" {
		t.Fatalf("按运行引用解析密钥失败：%#v", resolved)
	}
}

func TestCreateUsesFreshDataKeyAndNonceForEverySecret(t *testing.T) {
	repository := &memoryRepository{items: map[secret.ID]secret.StoredSecret{}}
	service := secret.NewService(repository, fixedKeyProvider())
	first, err := service.Create(context.Background(), "密钥一", []byte("same-value"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(context.Background(), "密钥二", []byte("same-value"))
	if err != nil {
		t.Fatal(err)
	}
	a := repository.items[first.ID]
	b := repository.items[second.ID]
	if bytes.Equal(a.Ciphertext, b.Ciphertext) || bytes.Equal(a.EncryptedDataKey, b.EncryptedDataKey) {
		t.Fatal("每条敏感值必须使用独立随机数据密钥和随机 nonce")
	}
}

func TestFileKeyProviderReadsBase64KeyFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x2a}, 32))
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := secret.NewFileKeyProvider(path, 7)
	key, err := provider.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if key.Version != 7 || len(key.Material) != 32 || key.Material[0] != 0x2a {
		t.Fatalf("主密钥文件解析错误：%+v", key)
	}
}

type memoryRepository struct {
	items map[secret.ID]secret.StoredSecret
}

func (r *memoryRepository) Create(_ context.Context, value secret.StoredSecret) (secret.Metadata, error) {
	r.items[value.ID] = value
	return value.Metadata, nil
}

func (r *memoryRepository) Load(_ context.Context, ids []secret.ID) ([]secret.StoredSecret, error) {
	result := make([]secret.StoredSecret, 0, len(ids))
	for _, id := range ids {
		if value, ok := r.items[id]; ok {
			result = append(result, value)
		}
	}
	return result, nil
}

func (r *memoryRepository) List(context.Context) ([]secret.Metadata, error) {
	result := make([]secret.Metadata, 0, len(r.items))
	for _, value := range r.items {
		result = append(result, value.Metadata)
	}
	return result, nil
}

type staticKeyProvider struct{ key secret.MasterKey }

func fixedKeyProvider() staticKeyProvider {
	return staticKeyProvider{key: secret.MasterKey{Version: 1, Material: bytes.Repeat([]byte{0x11}, 32)}}
}

func (p staticKeyProvider) Current(context.Context) (secret.MasterKey, error) { return p.key, nil }
func (p staticKeyProvider) ByVersion(_ context.Context, version int) (secret.MasterKey, error) {
	key := p.key
	key.Version = version
	return key, nil
}
