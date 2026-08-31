package backup

import (
	"testing"
)

func TestLoadConfigRequiresEverySecretFileAndEndpoint(t *testing.T) {
	valid := validBackupEnvironment()
	configuration, err := LoadConfig(mapEnvironment(valid))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Root != "/var/lib/yunling-ops" || configuration.COSRegion != "ap-guangzhou" {
		t.Fatalf("备份默认配置错误：%+v", configuration)
	}

	for _, key := range []string{
		"YUNLING_BACKUP_DATABASE_URL",
		"YUNLING_VERIFY_DATABASE_URL",
		"YUNLING_BACKUP_POSTGRES_PASSWORD_FILE",
		"YUNLING_VERIFY_POSTGRES_PASSWORD_FILE",
		"YUNLING_BACKUP_MINIO_ENDPOINT",
		"YUNLING_BACKUP_MINIO_BUCKET",
		"YUNLING_BACKUP_MINIO_ACCESS_KEY_FILE",
		"YUNLING_BACKUP_MINIO_SECRET_KEY_FILE",
		"YUNLING_COS_ENDPOINT",
		"YUNLING_COS_REGION",
		"YUNLING_COS_BUCKET",
		"YUNLING_COS_SECRET_ID_FILE",
		"YUNLING_COS_SECRET_KEY_FILE",
		"YUNLING_RESTIC_PASSWORD_FILE",
	} {
		values := cloneEnvironment(valid)
		delete(values, key)
		if _, err := LoadConfig(mapEnvironment(values)); err == nil {
			t.Fatalf("缺少 %s 必须失败", key)
		}
	}
}

func TestLoadConfigRejectsInlineSecretsAndInsecureCOS(t *testing.T) {
	for _, key := range []string{
		"YUNLING_BACKUP_POSTGRES_PASSWORD",
		"YUNLING_VERIFY_POSTGRES_PASSWORD",
		"YUNLING_BACKUP_MINIO_ACCESS_KEY",
		"YUNLING_BACKUP_MINIO_SECRET_KEY",
		"YUNLING_COS_SECRET_ID",
		"YUNLING_COS_SECRET_KEY",
		"YUNLING_RESTIC_PASSWORD",
	} {
		values := cloneEnvironment(validBackupEnvironment())
		values[key] = "不得出现在环境变量中的秘密"
		if _, err := LoadConfig(mapEnvironment(values)); err == nil {
			t.Fatalf("必须拒绝明文秘密环境变量 %s", key)
		}
	}

	values := cloneEnvironment(validBackupEnvironment())
	values["YUNLING_COS_ENDPOINT"] = "http://cos.ap-guangzhou.myqcloud.com"
	if _, err := LoadConfig(mapEnvironment(values)); err == nil {
		t.Fatal("COS endpoint 必须使用 HTTPS")
	}
}

func TestRequiredFreeBytesUsesTwoGiBOrOneAndAHalfTimesLatestBackup(t *testing.T) {
	const gib = int64(1024 * 1024 * 1024)
	if got := RequiredFreeBytes(0); got != 2*gib {
		t.Fatalf("无历史备份时至少需要 2 GiB，实际 %d", got)
	}
	if got := RequiredFreeBytes(4 * gib); got != 6*gib {
		t.Fatalf("大备份需要 1.5 倍空间，实际 %d", got)
	}
	if HasEnoughSpace(6*gib-1, 4*gib) || !HasEnoughSpace(6*gib, 4*gib) {
		t.Fatal("空间阈值判断错误")
	}
}

func validBackupEnvironment() map[string]string {
	return map[string]string{
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
		"YUNLING_COS_PREFIX":                    "yunling",
		"YUNLING_COS_SECRET_ID_FILE":            "/run/secrets/cos-secret-id",
		"YUNLING_COS_SECRET_KEY_FILE":           "/run/secrets/cos-secret-key",
		"YUNLING_RESTIC_PASSWORD_FILE":          "/run/secrets/restic-password",
	}
}

func mapEnvironment(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func cloneEnvironment(values map[string]string) map[string]string {
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
