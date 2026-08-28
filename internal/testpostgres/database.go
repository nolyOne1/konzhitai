package testpostgres

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Start(t testing.TB) *pgxpool.Pool {
	t.Helper()
	root := RepositoryRoot(t)
	basePath := filepath.Join(root, ".tools", "embedded-postgres")
	cachePath := filepath.Join(basePath, "cache")
	runtimeRoot := filepath.Join(basePath, "runtime")
	dataRoot := filepath.Join(basePath, "data")
	for _, path := range []string{cachePath, runtimeRoot, dataRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("创建 PostgreSQL 测试目录 %s：%v", path, err)
		}
	}

	testID := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	runtimePath := filepath.Join(runtimeRoot, testID)
	dataPath := filepath.Join(dataRoot, testID)
	port := availablePort(t)
	config := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V18).
		Port(port).
		Database("yunling_test").
		Username("postgres").
		Password("postgres").
		CachePath(cachePath).
		RuntimePath(runtimePath).
		DataPath(dataPath).
		Locale("C").
		Encoding("UTF8").
		StartTimeout(45 * time.Second).
		Logger(io.Discard)
	database := embeddedpostgres.NewDatabase(config)
	if err := database.Start(); err != nil {
		t.Fatalf("启动嵌入式 PostgreSQL：%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, config.GetConnectionURL())
	if err != nil {
		_ = database.Stop()
		t.Fatalf("连接嵌入式 PostgreSQL：%v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_ = database.Stop()
		t.Fatalf("检查嵌入式 PostgreSQL 连接：%v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		if err := database.Stop(); err != nil {
			t.Errorf("停止嵌入式 PostgreSQL：%v", err)
		}
		for _, path := range []string{runtimePath, dataPath} {
			if err := removeAllEventually(path, os.RemoveAll, time.Sleep); err != nil {
				t.Errorf("清理 PostgreSQL 测试目录 %s：%v", path, err)
			}
		}
	})
	return pool
}

func removeAllEventually(
	path string,
	remove func(string) error,
	pause func(time.Duration),
) error {
	const attempts = 40
	const retryDelay = 50 * time.Millisecond

	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if err = remove(path); err == nil {
			return nil
		}
		if attempt+1 < attempts {
			pause(retryDelay)
		}
	}
	return err
}

func ApplyInitialMigration(t testing.TB, db *pgxpool.Pool) {
	t.Helper()
	ApplyMigration(t, db, "000001_initial.up.sql")
}

func ApplyMigration(t testing.TB, db *pgxpool.Pool, name string) {
	t.Helper()
	path := filepath.Join(RepositoryRoot(t), "migrations", name)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取迁移 %s：%v", name, err)
	}
	if _, err := db.Exec(context.Background(), string(contents)); err != nil {
		t.Fatalf("执行迁移 %s：%v", name, err)
	}
}

func RepositoryRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取当前目录：%v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到仓库根目录")
		}
		dir = parent
	}
}

func availablePort(t testing.TB) uint32 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配 PostgreSQL 测试端口：%v", err)
	}
	defer listener.Close()
	return uint32(listener.Addr().(*net.TCPAddr).Port)
}
