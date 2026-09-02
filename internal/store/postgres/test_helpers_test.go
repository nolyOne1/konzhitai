package postgres

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/testpostgres"
)

func startPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testpostgres.Start(t)
}

func applyMigrations(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	root := testpostgres.RepositoryRoot(t)
	migrations, err := filepath.Glob(filepath.Join(root, "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("查找数据库迁移：%v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("未找到数据库迁移")
	}
	for _, path := range migrations {
		testpostgres.ApplyMigration(t, db, filepath.Base(path))
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
