package secret

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ID string

type creatorContextKey struct{}

var (
	ErrInvalidSecret  = errors.New("敏感参数内容无效")
	ErrSecretNotFound = errors.New("敏感参数不存在")
	ErrKeyUnavailable = errors.New("主密钥不可用")
)

type Metadata struct {
	ID        ID        `json:"id"`
	Name      string    `json:"name"`
	CreatedBy string    `json:"createdBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type StoredSecret struct {
	Metadata
	Ciphertext       []byte
	CiphertextNonce  []byte
	EncryptedDataKey []byte
	DataKeyNonce     []byte
	KeyVersion       int
}

type MasterKey struct {
	Version  int
	Material []byte
}

type KeyProvider interface {
	Current(context.Context) (MasterKey, error)
	ByVersion(context.Context, int) (MasterKey, error)
}

type Repository interface {
	Create(context.Context, StoredSecret) (Metadata, error)
	Load(context.Context, []ID) ([]StoredSecret, error)
	List(context.Context) ([]Metadata, error)
}

type Service struct {
	repository Repository
	keys       KeyProvider
	now        func() time.Time
	random     io.Reader
}

func NewService(repository Repository, keys KeyProvider) *Service {
	return &Service{repository: repository, keys: keys, now: time.Now, random: rand.Reader}
}

func WithCreator(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, creatorContextKey{}, strings.TrimSpace(userID))
}

func CreatorFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(creatorContextKey{}).(string)
	return userID, ok && userID != ""
}

func (s *Service) Create(ctx context.Context, name string, plaintext []byte) (Metadata, error) {
	name = strings.TrimSpace(name)
	if s == nil || s.repository == nil || s.keys == nil || name == "" || len(plaintext) == 0 {
		return Metadata{}, ErrInvalidSecret
	}
	master, err := s.keys.Current(ctx)
	if err != nil || !validMasterKey(master) {
		return Metadata{}, ErrKeyUnavailable
	}
	masterMaterial := append([]byte(nil), master.Material...)
	defer clear(masterMaterial)
	id := ID(uuid.NewString())
	now := s.now().UTC()
	createdBy, _ := ctx.Value(creatorContextKey{}).(string)
	metadata := Metadata{ID: id, Name: name, CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now}
	aad := associatedData(metadata, master.Version)

	dataKey := make([]byte, 32)
	if _, err := io.ReadFull(s.random, dataKey); err != nil {
		return Metadata{}, fmt.Errorf("生成敏感参数数据密钥：%w", err)
	}
	defer clear(dataKey)
	ciphertext, ciphertextNonce, err := seal(dataKey, plaintext, aad, s.random)
	if err != nil {
		return Metadata{}, fmt.Errorf("加密敏感参数：%w", err)
	}
	encryptedDataKey, dataKeyNonce, err := seal(masterMaterial, dataKey, aad, s.random)
	if err != nil {
		return Metadata{}, fmt.Errorf("包裹敏感参数数据密钥：%w", err)
	}
	return s.repository.Create(ctx, StoredSecret{
		Metadata: metadata, Ciphertext: ciphertext, CiphertextNonce: ciphertextNonce,
		EncryptedDataKey: encryptedDataKey, DataKeyNonce: dataKeyNonce, KeyVersion: master.Version,
	})
}

func (s *Service) ResolveForRun(ctx context.Context, refs []ID) (map[string]string, error) {
	if s == nil || s.repository == nil || s.keys == nil {
		return nil, ErrInvalidSecret
	}
	unique := make([]ID, 0, len(refs))
	seen := make(map[ID]bool, len(refs))
	for _, id := range refs {
		if strings.TrimSpace(string(id)) == "" {
			return nil, ErrInvalidSecret
		}
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	stored, err := s.repository.Load(ctx, unique)
	if err != nil {
		return nil, err
	}
	if len(stored) != len(unique) {
		return nil, ErrSecretNotFound
	}
	result := make(map[string]string, len(stored))
	for _, item := range stored {
		master, err := s.keys.ByVersion(ctx, item.KeyVersion)
		if err != nil || !validMasterKey(master) || master.Version != item.KeyVersion {
			return nil, ErrKeyUnavailable
		}
		masterMaterial := append([]byte(nil), master.Material...)
		aad := associatedData(item.Metadata, item.KeyVersion)
		dataKey, err := open(masterMaterial, item.DataKeyNonce, item.EncryptedDataKey, aad)
		clear(masterMaterial)
		if err != nil || len(dataKey) != 32 {
			return nil, ErrKeyUnavailable
		}
		plaintext, err := open(dataKey, item.CiphertextNonce, item.Ciphertext, aad)
		clear(dataKey)
		if err != nil {
			return nil, fmt.Errorf("解密敏感参数 %s：%w", item.ID, err)
		}
		result[string(item.ID)] = string(plaintext)
		clear(plaintext)
	}
	return result, nil
}

func (s *Service) List(ctx context.Context) ([]Metadata, error) {
	if s == nil || s.repository == nil {
		return nil, ErrInvalidSecret
	}
	items, err := s.repository.List(ctx)
	if items == nil && err == nil {
		items = []Metadata{}
	}
	return items, err
}

func validMasterKey(key MasterKey) bool {
	return key.Version > 0 && len(key.Material) == 32
}

func associatedData(metadata Metadata, version int) []byte {
	return []byte(fmt.Sprintf("yunling-secret\x00%s\x00%s\x00%d", metadata.ID, metadata.Name, version))
}

func seal(key, plaintext, aad []byte, random io.Reader) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, nil, err
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nonce, nil
}

func open(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, ErrKeyUnavailable
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}
