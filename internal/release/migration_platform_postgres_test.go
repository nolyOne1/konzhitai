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

func TestPlatformMigrationTransactionRollbackReceiptAndStructure(t *testing.T) {
	db := testpostgres.Start(t)
	ctx := context.Background()
	paths, err := filepath.Glob(filepath.Join(testpostgres.RepositoryRoot(t), "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var pending []byte
	for _, path := range paths {
		version, err := strconv.Atoi(filepath.Base(path)[:6])
		if err != nil {
			t.Fatal(err)
		}
		if version <= 15 {
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
	if _, err := db.Exec(ctx, `INSERT INTO servers(name,server_group_id) VALUES('保留节点','legacy-group')`); err != nil {
		t.Fatal(err)
	}
	recoveryPoint := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	if _, err := db.Exec(ctx, `INSERT INTO backup_runs(id,trigger_type,status,local_snapshot_id,cos_snapshot_id,manifest_sha256) VALUES($1,'manual','succeeded','local','cos',$2)`, recoveryPoint, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO restore_verifications(backup_run_id,trigger_type,status,migration_version) VALUES($1,'manual','succeeded','15')`, recoveryPoint); err != nil {
		t.Fatal(err)
	}
	current := StoredRelease{TargetID: "100", Compatibility: Compatibility{MigrationTreeSHA256: strings.Repeat("a", 64)}}
	target := StoredRelease{TargetID: "101", SourceSHA: strings.Repeat("b", 40), Compatibility: Compatibility{MigrationTreeSHA256: strings.Repeat("c", 64)}}
	receipt := platformMigrationReceipt(current, target, recoveryPoint, "release-admin")
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var preflight string
	if err := conn.QueryRow(ctx, string(platformMigrationPreflightSQL(recoveryPoint, receipt))).Scan(&preflight); err != nil || preflight != "15|t|0|0|f" {
		t.Fatalf("preflight %q %v", preflight, err)
	}
	transaction := string(platformMigrationTransaction(pending, receipt))
	if _, err := conn.Exec(ctx, strings.Replace(transaction, "COMMIT;", "SELECT 1/0; COMMIT;", 1)); err == nil {
		t.Fatal("fault injection did not fail")
	}
	if _, err := conn.Exec(ctx, "ROLLBACK;"); err != nil {
		t.Fatal(err)
	}
	var version, count int
	var absent bool
	if err := conn.QueryRow(ctx, `SELECT max(version),to_regclass('public.server_groups') IS NULL,(SELECT count(*) FROM audit_logs WHERE action='release.migration.apply') FROM schema_migrations`).Scan(&version, &absent, &count); err != nil {
		t.Fatal(err)
	}
	if version != 15 || !absent || count != 0 {
		t.Fatalf("schema or receipt survived rollback: %d %v %d", version, absent, count)
	}
	if _, err := conn.Exec(ctx, transaction); err != nil {
		t.Fatal(err)
	}
	var verification string
	if err := conn.QueryRow(ctx, string(platformMigrationVerificationSQL(receipt))).Scan(&verification); err != nil {
		t.Fatal(err)
	}
	if err := verifyMigrationState([]byte(verification), migrationPreState{}, 19); err != nil {
		t.Fatalf("real schema rejected: %s %v", verification, err)
	}
	if err := conn.QueryRow(ctx, string(platformMigrationPreflightSQL(recoveryPoint, receipt))).Scan(&preflight); err != nil || preflight != "19|t|0|0|t" {
		t.Fatalf("resume receipt %q %v", preflight, err)
	}
	target.SourceSHA = strings.Repeat("d", 40)
	otherReceipt := platformMigrationReceipt(current, target, recoveryPoint, "release-admin")
	if err := conn.QueryRow(ctx, string(platformMigrationPreflightSQL(recoveryPoint, otherReceipt))).Scan(&preflight); err != nil || preflight != "19|t|0|0|f" {
		t.Fatalf("wrong source accepted %q %v", preflight, err)
	}
	if _, err := conn.Exec(ctx, "DROP INDEX log_chunks_archive_cursor_idx"); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, string(platformMigrationVerificationSQL(receipt))).Scan(&verification); err != nil {
		t.Fatal(err)
	}
	if verifyMigrationState([]byte(verification), migrationPreState{}, 19) == nil {
		t.Fatal("missing new index was accepted")
	}
}
