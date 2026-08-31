//go:build !windows

package backup

import "syscall"

func availableDiskBytes(path string) (int64, error) {
	var statistics syscall.Statfs_t
	if err := syscall.Statfs(path, &statistics); err != nil {
		return 0, err
	}
	return int64(statistics.Bavail) * int64(statistics.Bsize), nil
}
