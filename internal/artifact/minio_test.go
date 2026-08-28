package artifact

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/minio/minio-go/v7"
)

func TestMinIOStoreReusesVerifiedContentAddressedObject(t *testing.T) {
	client := &fakeObjectClient{
		stat: minio.ObjectInfo{
			Key:          "scripts/script-1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.tar.gz",
			Size:         4,
			UserMetadata: minio.StringMap{"Sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
	}
	store := newMinIOStoreWithClient(client, "yunling-artifacts")
	key := client.stat.Key

	err := store.Put(context.Background(), key, bytes.NewReader([]byte("data")), 4, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("复用已校验对象：%v", err)
	}
	if client.putCalls != 0 {
		t.Fatalf("已存在且校验一致的对象不应重复上传，实际上传 %d 次", client.putCalls)
	}
}

func TestMinIOStoreRejectsContentAddressCollision(t *testing.T) {
	client := &fakeObjectClient{
		stat: minio.ObjectInfo{
			Key:          "scripts/script-1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.tar.gz",
			Size:         5,
			UserMetadata: minio.StringMap{"Sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}
	store := newMinIOStoreWithClient(client, "yunling-artifacts")

	err := store.Put(context.Background(), client.stat.Key, bytes.NewReader([]byte("data")), 4, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil {
		t.Fatal("同一对象键指向不同内容时必须拒绝覆盖")
	}
	if client.putCalls != 0 {
		t.Fatal("发生内容地址冲突时不得上传")
	}
}

type fakeObjectClient struct {
	stat     minio.ObjectInfo
	statErr  error
	putCalls int
}

func (c *fakeObjectClient) PutObject(_ context.Context, _, _ string, body io.Reader, _ int64, _ minio.PutObjectOptions) (minio.UploadInfo, error) {
	c.putCalls++
	_, _ = io.Copy(io.Discard, body)
	return minio.UploadInfo{}, nil
}

func (c *fakeObjectClient) StatObject(context.Context, string, string, minio.StatObjectOptions) (minio.ObjectInfo, error) {
	return c.stat, c.statErr
}

func (c *fakeObjectClient) GetObject(context.Context, string, string, minio.GetObjectOptions) (io.ReadCloser, error) {
	if c.statErr != nil && !errors.Is(c.statErr, errObjectMissing) {
		return nil, c.statErr
	}
	return io.NopCloser(bytes.NewReader(nil)), nil
}
