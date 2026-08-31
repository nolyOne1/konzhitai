package backup

import (
	"errors"
	"math"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const defaultBackupRoot = "/var/lib/yunling-ops"

type Config struct {
	BackupDatabaseURL          string
	VerificationDatabaseURL    string
	BackupDatabasePasswordFile string
	VerifyDatabasePasswordFile string
	MinIOEndpoint              string
	MinIOBucket                string
	MinIOAccessKeyFile         string
	MinIOSecretKeyFile         string
	COSEndpoint                string
	COSRegion                  string
	COSBucket                  string
	COSPrefix                  string
	COSSecretIDFile            string
	COSSecretKeyFile           string
	ResticPasswordFile         string
	LocalRepositoryFile        string
	COSRepositoryFile          string
	Root                       string
	CommandTimeout             time.Duration
}

func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, ErrInvalidRequest
	}
	for _, key := range []string{
		"YUNLING_BACKUP_POSTGRES_PASSWORD",
		"YUNLING_VERIFY_POSTGRES_PASSWORD",
		"YUNLING_BACKUP_MINIO_ACCESS_KEY",
		"YUNLING_BACKUP_MINIO_SECRET_KEY",
		"YUNLING_COS_SECRET_ID",
		"YUNLING_COS_SECRET_KEY",
		"YUNLING_RESTIC_PASSWORD",
	} {
		if strings.TrimSpace(getenv(key)) != "" {
			return Config{}, errors.New("备份秘密只允许通过受限文件提供")
		}
	}
	configuration := Config{
		BackupDatabaseURL:          strings.TrimSpace(getenv("YUNLING_BACKUP_DATABASE_URL")),
		VerificationDatabaseURL:    strings.TrimSpace(getenv("YUNLING_VERIFY_DATABASE_URL")),
		BackupDatabasePasswordFile: strings.TrimSpace(getenv("YUNLING_BACKUP_POSTGRES_PASSWORD_FILE")),
		VerifyDatabasePasswordFile: strings.TrimSpace(getenv("YUNLING_VERIFY_POSTGRES_PASSWORD_FILE")),
		MinIOEndpoint:              strings.TrimSpace(getenv("YUNLING_BACKUP_MINIO_ENDPOINT")),
		MinIOBucket:                strings.TrimSpace(getenv("YUNLING_BACKUP_MINIO_BUCKET")),
		MinIOAccessKeyFile:         strings.TrimSpace(getenv("YUNLING_BACKUP_MINIO_ACCESS_KEY_FILE")),
		MinIOSecretKeyFile:         strings.TrimSpace(getenv("YUNLING_BACKUP_MINIO_SECRET_KEY_FILE")),
		COSEndpoint:                strings.TrimSpace(getenv("YUNLING_COS_ENDPOINT")),
		COSRegion:                  strings.TrimSpace(getenv("YUNLING_COS_REGION")),
		COSBucket:                  strings.TrimSpace(getenv("YUNLING_COS_BUCKET")),
		COSPrefix:                  strings.Trim(strings.TrimSpace(getenv("YUNLING_COS_PREFIX")), "/"),
		COSSecretIDFile:            strings.TrimSpace(getenv("YUNLING_COS_SECRET_ID_FILE")),
		COSSecretKeyFile:           strings.TrimSpace(getenv("YUNLING_COS_SECRET_KEY_FILE")),
		ResticPasswordFile:         strings.TrimSpace(getenv("YUNLING_RESTIC_PASSWORD_FILE")),
		LocalRepositoryFile:        "/run/config/local-repository",
		COSRepositoryFile:          "/run/config/cos-repository",
		Root:                       defaultBackupRoot,
		CommandTimeout:             2 * time.Hour,
	}
	if root := strings.TrimSpace(getenv("YUNLING_BACKUP_ROOT")); root != "" {
		configuration.Root = filepath.Clean(root)
	}
	if configuration.COSPrefix == "" {
		configuration.COSPrefix = "yunling"
	}
	for _, value := range []string{
		configuration.BackupDatabaseURL,
		configuration.VerificationDatabaseURL,
		configuration.BackupDatabasePasswordFile,
		configuration.VerifyDatabasePasswordFile,
		configuration.MinIOEndpoint,
		configuration.MinIOBucket,
		configuration.MinIOAccessKeyFile,
		configuration.MinIOSecretKeyFile,
		configuration.COSEndpoint,
		configuration.COSRegion,
		configuration.COSBucket,
		configuration.COSSecretIDFile,
		configuration.COSSecretKeyFile,
		configuration.ResticPasswordFile,
	} {
		if value == "" {
			return Config{}, errors.New("备份配置不完整")
		}
	}
	for _, databaseURL := range []string{configuration.BackupDatabaseURL, configuration.VerificationDatabaseURL} {
		parsed, err := url.Parse(databaseURL)
		if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
			return Config{}, errors.New("备份数据库地址无效")
		}
		if parsed.User != nil {
			if _, present := parsed.User.Password(); present {
				return Config{}, errors.New("数据库密码只允许通过受限文件提供")
			}
		}
	}
	minioURL, err := url.Parse(configuration.MinIOEndpoint)
	if err != nil || (minioURL.Scheme != "http" && minioURL.Scheme != "https") || minioURL.Host == "" {
		return Config{}, errors.New("MinIO endpoint 无效")
	}
	cosURL, err := url.Parse(configuration.COSEndpoint)
	if err != nil || cosURL.Scheme != "https" || cosURL.Host == "" || cosURL.Path != "" {
		return Config{}, errors.New("COS endpoint 必须是 HTTPS 根地址")
	}
	if !isAbsoluteConfigPath(configuration.Root) {
		return Config{}, errors.New("备份根目录必须是绝对路径")
	}
	for _, path := range []string{
		configuration.BackupDatabasePasswordFile,
		configuration.VerifyDatabasePasswordFile,
		configuration.MinIOAccessKeyFile,
		configuration.MinIOSecretKeyFile,
		configuration.COSSecretIDFile,
		configuration.COSSecretKeyFile,
		configuration.ResticPasswordFile,
		configuration.LocalRepositoryFile,
		configuration.COSRepositoryFile,
	} {
		if !isAbsoluteConfigPath(path) {
			return Config{}, errors.New("备份秘密和仓库配置必须使用绝对路径")
		}
	}
	return configuration, nil
}

func isAbsoluteConfigPath(value string) bool {
	return filepath.IsAbs(value) || (strings.HasPrefix(value, "/") && !strings.ContainsRune(value, '\x00'))
}

func RequiredFreeBytes(latestSuccessfulBackupBytes int64) int64 {
	const twoGiB = int64(2 * 1024 * 1024 * 1024)
	if latestSuccessfulBackupBytes <= 0 {
		return twoGiB
	}
	if latestSuccessfulBackupBytes > math.MaxInt64/3*2 {
		return math.MaxInt64
	}
	required := latestSuccessfulBackupBytes + latestSuccessfulBackupBytes/2
	if required < twoGiB {
		return twoGiB
	}
	return required
}

func HasEnoughSpace(availableBytes, latestSuccessfulBackupBytes int64) bool {
	return availableBytes >= RequiredFreeBytes(latestSuccessfulBackupBytes)
}
