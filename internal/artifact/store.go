package artifact

import (
	"context"
	"errors"
	"io"
)

var (
	ErrObjectMissing  = errors.New("对象不存在")
	ErrObjectConflict = errors.New("内容寻址对象与已保存内容不一致，已拒绝覆盖")
)

// Store 保存不可变、按内容寻址的脚本包。
type Store interface {
	Put(ctx context.Context, key string, body io.Reader, size int64, sha256 string) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}
