//go:build linux

package testpostgres

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"golang.org/x/sys/unix"
)

func preparedBinaries(config embeddedpostgres.Config, root string) (string, error) {
	key, err := binaryCacheKey()
	if err != nil {
		return "", err
	}
	return ensureBinaryDirectory(root, key, func(destination string) error {
		return initializeBinaries(config, destination)
	})
}

// Match the platform variants selected by embedded-postgres v1.34.0. Bump
// the format/dependency prefix when changing the preparation contract.
func binaryCacheKey() (string, error) {
	arch := runtime.GOARCH
	if arch == "arm64" {
		arch = "arm64v8"
	} else if arch == "arm" {
		machine, err := exec.Command("uname", "-m").Output()
		if err != nil {
			return "", fmt.Errorf("确定 PostgreSQL ARM 平台：%w", err)
		}
		if strings.HasPrefix(string(machine), "armv7") {
			arch = "arm32v7"
		} else if strings.HasPrefix(string(machine), "armv6") {
			arch = "arm32v6"
		}
	}
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		arch += "-alpine"
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "embedded-postgres-1.34.0-v1-linux-" + arch + "-" + string(embeddedpostgres.V18), nil
}

func ensureBinaryDirectory(root, key string, prepare func(string) error) (string, error) {
	if key == "" || filepath.Base(key) != key || key == "." || key == ".." {
		return "", errors.New("PostgreSQL 二进制缓存键无效")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	// The lock inode must remain in place: deleting it while another process
	// waits would allow a third process to lock a different inode.
	descriptor, err := unix.Open(filepath.Join(root, key+".lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	defer unix.Close(descriptor)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err = unix.Flock(descriptor, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", errors.New("等待 PostgreSQL 二进制缓存初始化锁超时")
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer unix.Flock(descriptor, unix.LOCK_UN)

	destination := filepath.Join(root, key)
	if _, err := os.Lstat(destination); err == nil {
		if err := validateBinaryDirectory(destination, key); err != nil {
			return "", fmt.Errorf("已发布的 PostgreSQL 缓存不完整，停止复用：%w", err)
		}
		return destination, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	staging, err := os.MkdirTemp(root, key+"-staging-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	if err := prepare(staging); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(staging, ".ready"), []byte(key+"\n"), 0o644); err != nil {
		return "", err
	}
	if err := validateBinaryDirectory(staging, key); err != nil {
		return "", err
	}
	// No consumer sees individual files while extraction is in progress. Only
	// publish after the initializer has successfully started and stopped PG.
	if err := os.Rename(staging, destination); err != nil {
		return "", err
	}
	return destination, nil
}

func validateBinaryDirectory(path, key string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("缓存目录不可用：%s", path)
	}
	ready, err := os.ReadFile(filepath.Join(path, ".ready"))
	if err != nil || string(ready) != key+"\n" {
		return errors.New("缺少匹配版本的完整准备标记")
	}
	for _, name := range []string{"pg_ctl", "initdb", "postgres"} {
		info, err := os.Lstat(filepath.Join(path, "bin", name))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			return fmt.Errorf("PostgreSQL 可执行文件不可用：%s", name)
		}
	}
	return nil
}

// The upstream public API prepares binaries as part of Start. Use a throwaway
// cluster exactly once per cache, stop it before publishing its binaries, and
// keep every real test's runtime/data/port separate as before.
func initializeBinaries(config embeddedpostgres.Config, destination string) (resultErr error) {
	work, err := os.MkdirTemp(filepath.Dir(destination), ".postgres-init-")
	if err != nil {
		return err
	}
	defer func() {
		if err := removeAllEventually(work, os.RemoveAll, time.Sleep); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("清理 PostgreSQL 准备目录：%w", err))
		}
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := uint32(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		return err
	}
	database := embeddedpostgres.NewDatabase(config.BinariesPath(destination).
		RuntimePath(filepath.Join(work, "runtime")).DataPath(filepath.Join(work, "data")).Port(port))
	if err := database.Start(); err != nil {
		stopErr := database.Stop()
		if errors.Is(stopErr, embeddedpostgres.ErrServerNotStarted) {
			stopErr = nil
		}
		return errors.Join(err, stopErr)
	}
	return database.Stop()
}
