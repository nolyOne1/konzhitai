package postgres

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func startPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()

	root := repositoryRoot(t)
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
		StartTimeout(45 * time.Second).
		Logger(testWriter{t: t})
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
			if err := os.RemoveAll(path); err != nil {
				t.Errorf("清理 PostgreSQL 测试目录 %s：%v", path, err)
			}
		}
	})

	return pool
}

func applyMigrations(t *testing.T, db *pgxpool.Pool) {
	t.Helper()

	path := filepath.Join(repositoryRoot(t), "migrations", "000001_initial.up.sql")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取初始迁移：%v", err)
	}
	if _, err := db.Exec(context.Background(), string(contents)); err != nil {
		t.Fatalf("执行初始迁移：%v", err)
	}
}

func tableExists(t *testing.T, db *pgxpool.Pool, table string) bool {
	t.Helper()

	var exists bool
	err := db.QueryRow(
		context.Background(),
		`SELECT to_regclass('public.' || $1) IS NOT NULL`,
		table,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("检查数据表 %s：%v", table, err)
	}
	return exists
}

func repositoryRoot(t *testing.T) string {
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

func availablePort(t *testing.T) uint32 {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配 PostgreSQL 测试端口：%v", err)
	}
	defer listener.Close()
	return uint32(listener.Addr().(*net.TCPAddr).Port)
}

type testWriter struct {
	t *testing.T
}

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("PostgreSQL: %s", p)
	return len(p), nil
}
