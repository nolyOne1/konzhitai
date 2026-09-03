//go:build linux

package release

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func platformFreeBytes(path string) (uint64, error) {
	var status unix.Statfs_t
	if err := unix.Statfs(path, &status); err != nil {
		return 0, err
	}
	if status.Bsize <= 0 || status.Bavail > math.MaxUint64/uint64(status.Bsize) {
		return 0, errors.New("文件系统可用空间数值无效")
	}
	return status.Bavail * uint64(status.Bsize), nil
}

func platformAvailableMemory() (uint64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer file.Close()
	return parseAvailableMemory(file)
}

func platformTryLock(path string) (func() error, error) {
	if path == "" {
		return nil, errors.New("发布锁路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("创建发布锁目录：%w", err)
	}
	descriptor, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开发布锁：%w", err)
	}
	if err := unix.Flock(descriptor, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(descriptor)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("获取发布锁：%w", err)
	}
	released := false
	return func() error {
		if released {
			return nil
		}
		released = true
		unlockErr := unix.Flock(descriptor, unix.LOCK_UN)
		closeErr := unix.Close(descriptor)
		if unlockErr != nil {
			return fmt.Errorf("释放发布锁：%w", unlockErr)
		}
		if closeErr != nil {
			return fmt.Errorf("关闭发布锁：%w", closeErr)
		}
		return nil
	}, nil
}
