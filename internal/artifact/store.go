package artifact

import (
	"context"
	"io"
)

// Store 保存不可变、按内容寻址的脚本包。
type Store interface {
	Put(ctx context.Context, key string, body io.Reader, size int64, sha256 string) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}
