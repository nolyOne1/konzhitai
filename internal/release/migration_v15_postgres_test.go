package release

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"yunling.local/platform/internal/testpostgres"
)

func TestV15TransactionRollsBackAndVerifiesRealDatabase(t *testing.T) {
	db := testpostgres.Start(t)
	ctx := context.Background()
	root := filepath.Join(testpostgres.RepositoryRoot(t), "migrations")
	paths, err := filepath.Glob(filepath.Join(root, "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var pending []byte
	for _, path := range paths {
		version, err := strconv.Atoi(filepath.Base(path)[:6])
		if err != nil {
			t.Fatal(err)
		}
		if version <= 12 {
			testpostgres.ApplyMigration(t, db, filepath.Base(path))
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		pending = append(pending, body...)
		pending = append(pending, '\n')
	}
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// Force failure after all three migrations: none of their schema changes may survive.
	if _, err := conn.Exec(ctx, "BEGIN;\n"+string(pending)+"SELECT 1/0;\nCOMMIT;"); err == nil {
		t.Fatal("故障注入未失败")
	}
	if _, err := conn.Exec(ctx, "ROLLBACK;"); err != nil {
		t.Fatal(err)
	}
	var version int
	var absent bool
	if err := conn.QueryRow(ctx, "SELECT max(version), to_regclass('public.agent_releases') IS NULL FROM schema_migrations").Scan(&version, &absent); err != nil {
		t.Fatal(err)
	}
	if version != 12 || !absent {
		t.Fatalf("事务未回滚：%d %v", version, absent)
	}
	if _, err := conn.Exec(ctx, "BEGIN;\n"+string(pending)+"COMMIT;"); err != nil {
		t.Fatal(err)
	}
	var result string
	if err := conn.QueryRow(ctx, string(migrationVerificationSQL(15))).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if err := verifyMigrationState([]byte(result), migrationPreState{}, 15); err != nil {
		t.Fatalf("真实结构校验失败 %s: %v", result, err)
	}
	if _, err := conn.Exec(ctx, "DROP INDEX agent_upgrade_events_command_stage_idx"); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, string(migrationVerificationSQL(15))).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if verifyMigrationState([]byte(result), migrationPreState{}, 15) == nil {
		t.Fatal("缺少升级幂等索引必须拒绝")
	}
	if !strings.HasPrefix(result, "15|") {
		t.Fatal(result)
	}
}
