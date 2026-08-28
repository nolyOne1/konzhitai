package postgres

import (
	"context"
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
	testpostgres.ApplyInitialMigration(t, db)
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
