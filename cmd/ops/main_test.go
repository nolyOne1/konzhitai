package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/backup"
)

func TestConfigRequiresDatabaseAndMasterKeyAndDefaultsToFifteenSeconds(t *testing.T) {
	values := validOpsEnvironment()
	config, err := loadConfig(mapEnv(values))
	if err != nil {
		t.Fatal(err)
	}
	if config.ScanInterval != 15*time.Second || config.HTTPAddress != ":8081" || config.MasterKeyVersion != 1 {
		t.Fatalf("默认配置错误：%+v", config)
	}
	for _, key := range []string{"YUNLING_DATABASE_URL", "YUNLING_MASTER_KEY_FILE"} {
		incomplete := validOpsEnvironment()
		delete(incomplete, key)
		if _, err := loadConfig(mapEnv(incomplete)); err == nil {
			t.Fatalf("缺少必要配置 %s 必须失败", key)
		}
	}
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func validOpsEnvironment() map[string]string {
	return map[string]string{
		"YUNLING_DATABASE_URL":                  "postgres://ops@postgres:5432/yunling?sslmode=disable",
		"YUNLING_MASTER_KEY_FILE":               "/run/secrets/yunling-master-key",
		"YUNLING_BACKUP_DATABASE_URL":           "postgres://backup@postgres:5432/yunling?sslmode=disable",
		"YUNLING_VERIFY_DATABASE_URL":           "postgres://verifier@postgres:5432/postgres?sslmode=disable",
		"YUNLING_BACKUP_POSTGRES_PASSWORD_FILE": "/run/secrets/backup-postgres-password",
		"YUNLING_VERIFY_POSTGRES_PASSWORD_FILE": "/run/secrets/verify-postgres-password",
		"YUNLING_BACKUP_MINIO_ENDPOINT":         "http://minio:9000",
		"YUNLING_BACKUP_MINIO_BUCKET":           "yunling",
		"YUNLING_BACKUP_MINIO_ACCESS_KEY_FILE":  "/run/secrets/backup-minio-access-key",
		"YUNLING_BACKUP_MINIO_SECRET_KEY_FILE":  "/run/secrets/backup-minio-secret-key",
		"YUNLING_COS_ENDPOINT":                  "https://cos.ap-guangzhou.myqcloud.com",
		"YUNLING_COS_REGION":                    "ap-guangzhou",
		"YUNLING_COS_BUCKET":                    "yunling-backup-1250000000",
		"YUNLING_COS_SECRET_ID_FILE":            "/run/secrets/cos-secret-id",
		"YUNLING_COS_SECRET_KEY_FILE":           "/run/secrets/cos-secret-key",
		"YUNLING_RESTIC_PASSWORD_FILE":          "/run/secrets/restic-password",
	}
}

type fakeToolRunner struct{}

func (fakeToolRunner) Run(_ context.Context, name string, _ []string, _ map[string]string) (backup.CommandResult, error) {
	return backup.CommandResult{Stdout: name + " version-for-test", ExitCode: 0}, nil
}

func TestCheckToolsPrintsOnlyToolNamesAndVersions(t *testing.T) {
	var output bytes.Buffer
	if err := checkTools(context.Background(), &output, fakeToolRunner{}); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, name := range []string{"pg_dump", "pg_restore", "psql", "mc", "restic"} {
		if !strings.Contains(text, name) {
			t.Fatalf("工具检查输出缺少 %s：%q", name, text)
		}
	}
	if strings.Contains(text, "YUNLING_") || strings.Contains(text, "/run/secrets") {
		t.Fatalf("工具检查不得输出配置：%q", text)
	}
}
