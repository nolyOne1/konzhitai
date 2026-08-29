package secret_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/secret"
	"yunling.local/platform/internal/testpostgres"
)

func TestPostgresRepositoryPersistsOnlyEnvelopeCiphertext(t *testing.T) {
	db := secretDatabase(t)
	ctx := context.Background()
	userID := insertSecretUser(t, db)
	repository := secret.NewPostgresRepository(db)
	service := secret.NewService(repository, fixedKeyProvider())
	plaintext := []byte("postgres-only-secret")

	metadata, err := service.Create(secret.WithCreator(ctx, userID), "生产访问令牌", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext, ciphertextNonce, encryptedDataKey, dataKeyNonce []byte
	var keyVersion int
	if err := db.QueryRow(ctx, `
		SELECT ciphertext, nonce, encrypted_data_key, data_key_nonce, key_version
		FROM secrets WHERE id=$1
	`, metadata.ID).Scan(&ciphertext, &ciphertextNonce, &encryptedDataKey, &dataKeyNonce, &keyVersion); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, plaintext) || bytes.Contains(encryptedDataKey, plaintext) {
		t.Fatal("PostgreSQL 不得保存敏感参数明文")
	}
	if len(ciphertextNonce) != 12 || len(dataKeyNonce) != 12 || len(encryptedDataKey) == 0 || keyVersion != 1 {
		t.Fatalf("数据库中的信封加密字段不完整：cipherNonce=%d dataNonce=%d encryptedKey=%d version=%d", len(ciphertextNonce), len(dataKeyNonce), len(encryptedDataKey), keyVersion)
	}
	listed, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != metadata.ID || listed[0].CreatedBy != userID {
		t.Fatalf("敏感参数元数据不完整：%+v", listed)
	}
	resolved, err := service.ResolveForRun(ctx, []secret.ID{metadata.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resolved[string(metadata.ID)] != string(plaintext) {
		t.Fatalf("数据库密文无法正确解密：%#v", resolved)
	}
}

func TestRunValueSourceRequiresMatchingExecutionTokenAndTaskBinding(t *testing.T) {
	db := secretDatabase(t)
	ctx := context.Background()
	userID := insertSecretUser(t, db)
	service := secret.NewService(secret.NewPostgresRepository(db), fixedKeyProvider())
	bound, err := service.Create(secret.WithCreator(ctx, userID), "任务专用令牌", []byte("run-bound-secret"))
	if err != nil {
		t.Fatal(err)
	}
	unbound, err := service.Create(secret.WithCreator(ctx, userID), "其他令牌", []byte("must-not-leak"))
	if err != nil {
		t.Fatal(err)
	}
	runID, token := insertSecretBoundRun(t, db, userID, map[string]string{"API_TOKEN": string(bound.ID)})
	source := secret.NewRunValueSource(db, service)

	values, err := source.ValuesForRun(ctx, runID, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || string(values[0]) != "run-bound-secret" {
		t.Fatalf("只应返回当前任务引用的敏感值：%q", values)
	}
	for _, value := range values {
		if string(value) == "must-not-leak" || string(value) == string(unbound.ID) {
			t.Fatal("未绑定的敏感参数不得泄露给运行实例")
		}
	}
	_, err = source.ValuesForRun(ctx, runID, "wrong-token")
	if !errors.Is(err, secret.ErrRunAccessDenied) {
		t.Fatalf("错误执行令牌应被拒绝，实际错误：%v", err)
	}
}

func secretDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	testpostgres.ApplyMigration(t, db, "000002_agent_enrollment.up.sql")
	testpostgres.ApplyMigration(t, db, "000005_task_scheduling.up.sql")
	testpostgres.ApplyMigration(t, db, "000006_scheduler_resources.up.sql")
	testpostgres.ApplyMigration(t, db, "000007_run_observability.up.sql")
	testpostgres.ApplyMigration(t, db, "000008_security_audit_alerts.up.sql")
	return db
}

func insertSecretUser(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ($1, '安全管理员', 'test-hash') RETURNING id
	`, fmt.Sprintf("secret-%d@example.com", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertSecretBoundRun(t *testing.T, db *pgxpool.Pool, userID string, bindings map[string]string) (string, string) {
	t.Helper()
	ctx := context.Background()
	var scriptID, versionID, definitionID, runID string
	if err := db.QueryRow(ctx, `
		INSERT INTO scripts (name, runtime, created_by) VALUES ($1, 'bash', $2) RETURNING id
	`, fmt.Sprintf("敏感参数测试脚本-%d", time.Now().UnixNano()), userID).Scan(&scriptID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO script_versions (script_id, version, artifact_uri, artifact_sha256, entrypoint, created_by)
		VALUES ($1, 1, $2, repeat('a', 64), 'main.sh', $3) RETURNING id
	`, scriptID, fmt.Sprintf("scripts/%s/v1.tar.gz", scriptID), userID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	bindingJSON, err := json.Marshal(bindings)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO task_definitions (name, script_id, required_runtime, secret_bindings, created_by)
		VALUES ($1, $2, 'bash', $3, $4) RETURNING id
	`, fmt.Sprintf("敏感参数测试任务-%d", time.Now().UnixNano()), scriptID, bindingJSON, userID).Scan(&definitionID); err != nil {
		t.Fatal(err)
	}
	token := "execution-token-for-secret-test"
	if err := db.QueryRow(ctx, `
		INSERT INTO task_runs (
			task_definition_id, script_version_id, requested_by, trigger_type,
			required_runtime, required_labels, execution_token
		) VALUES ($1, $2, $3, 'manual', 'bash', '{}', $4) RETURNING id
	`, definitionID, versionID, userID, token).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	return runID, token
}
