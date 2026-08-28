package artifact

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var errObjectMissing = errors.New("对象不存在")

type MinIOConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Secure    bool
}

type minioObjectClient interface {
	PutObject(context.Context, string, string, io.Reader, int64, minio.PutObjectOptions) (minio.UploadInfo, error)
	StatObject(context.Context, string, string, minio.StatObjectOptions) (minio.ObjectInfo, error)
	GetObject(context.Context, string, string, minio.GetObjectOptions) (io.ReadCloser, error)
}

type MinIOStore struct {
	client minioObjectClient
	bucket string
}

func NewMinIOStore(config MinIOConfig) (*MinIOStore, error) {
	config.Endpoint = strings.TrimSpace(config.Endpoint)
	config.AccessKey = strings.TrimSpace(config.AccessKey)
	config.Bucket = strings.TrimSpace(config.Bucket)
	if config.Endpoint == "" || config.AccessKey == "" || config.SecretKey == "" || config.Bucket == "" {
		return nil, errors.New("MinIO 对象存储配置不完整")
	}
	client, err := minio.New(config.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(config.AccessKey, config.SecretKey, ""),
		Secure: config.Secure,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 MinIO 客户端：%w", err)
	}
	return newMinIOStoreWithClient(minioClientAdapter{Client: client}, config.Bucket), nil
}

func newMinIOStoreWithClient(client minioObjectClient, bucket string) *MinIOStore {
	return &MinIOStore{client: client, bucket: bucket}
}

func (s *MinIOStore) Put(ctx context.Context, key string, body io.Reader, size int64, checksum string) error {
	if !validObjectKey(key) || body == nil || size < 0 || !validSHA256(checksum) {
		return errors.New("脚本对象参数无效")
	}
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return verifyObject(info, size, checksum)
	}
	if !isMissingObject(err) {
		return fmt.Errorf("检查脚本对象：%w", err)
	}

	options := minio.PutObjectOptions{
		ContentType:    "application/gzip",
		SendContentMd5: true,
		UserMetadata:   map[string]string{"sha256": checksum},
	}
	options.SetMatchETagExcept("*")
	if _, err := s.client.PutObject(ctx, s.bucket, key, body, size, options); err != nil {
		if response := minio.ToErrorResponse(err); response.Code != "PreconditionFailed" {
			return fmt.Errorf("上传脚本对象：%w", err)
		}
	}
	info, err = s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return fmt.Errorf("校验脚本对象：%w", err)
	}
	return verifyObject(info, size, checksum)
}

func (s *MinIOStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if !validObjectKey(key) {
		return nil, errors.New("脚本对象键无效")
	}
	if _, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{}); err != nil {
		if isMissingObject(err) {
			return nil, errObjectMissing
		}
		return nil, fmt.Errorf("检查脚本对象：%w", err)
	}
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("打开脚本对象：%w", err)
	}
	return object, nil
}

func verifyObject(info minio.ObjectInfo, size int64, checksum string) error {
	storedChecksum := ""
	for key, value := range info.UserMetadata {
		if strings.EqualFold(key, "sha256") {
			storedChecksum = value
			break
		}
	}
	if info.Size != size || !strings.EqualFold(storedChecksum, checksum) {
		return errors.New("内容寻址对象与已保存内容不一致，已拒绝覆盖")
	}
	return nil
}

func validObjectKey(key string) bool {
	cleaned := path.Clean(key)
	return key != "" && cleaned == key && cleaned != "." && !strings.HasPrefix(cleaned, "/") && !strings.HasPrefix(cleaned, "../") && !strings.Contains(cleaned, "\\")
}

func validSHA256(value string) bool {
	if len(value) != sha256HexLength {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256ByteLength
}

const (
	sha256HexLength  = 64
	sha256ByteLength = 32
)

func isMissingObject(err error) bool {
	if errors.Is(err, errObjectMissing) {
		return true
	}
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NoSuchObject", "NotFound":
		return true
	default:
		return false
	}
}

type minioClientAdapter struct {
	*minio.Client
}

func (a minioClientAdapter) GetObject(ctx context.Context, bucket, key string, options minio.GetObjectOptions) (io.ReadCloser, error) {
	return a.Client.GetObject(ctx, bucket, key, options)
}

var _ Store = (*MinIOStore)(nil)
