package testpostgres

import (
	"errors"
	"testing"
	"time"
)

func TestRemoveAllEventuallyRetriesTransientWindowsFileLocks(t *testing.T) {
	locked := errors.New("文件仍被占用")
	var attempts int
	var pauses int

	err := removeAllEventually(
		"临时数据库目录",
		func(string) error {
			attempts++
			if attempts < 3 {
				return locked
			}
			return nil
		},
		func(time.Duration) { pauses++ },
	)

	if err != nil {
		t.Fatalf("短暂占用释放后清理应成功：%v", err)
	}
	if attempts != 3 || pauses != 2 {
		t.Fatalf("应重试到第三次成功，attempts=%d pauses=%d", attempts, pauses)
	}
}
