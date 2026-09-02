package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var temporaryDatabasePattern = regexp.MustCompile(`^yunling_verify_[0-9a-f]{32}$`)

type COSRestorer interface {
	RestoreFromCOS(context.Context, string, string) error
}

type BackupVerifier interface {
	Verify(context.Context, RestoreVerification, BackupRun) (VerificationResult, error)
}

type Verifier struct {
	configuration Config
	restorer      COSRestorer
	runner        CommandExecutor
	paths         RunPaths
	newDatabase   func() string
}

func NewVerifier(configuration Config, restorer COSRestorer, runner CommandExecutor, paths RunPaths) *Verifier {
	return &Verifier{
		configuration: configuration, restorer: restorer, runner: runner, paths: paths,
		newDatabase: func() string { return "yunling_verify_" + strings.ReplaceAll(uuid.NewString(), "-", "") },
	}
}

func ValidateTemporaryDatabase(name, productionDatabase string) error {
	if !temporaryDatabasePattern.MatchString(name) || strings.EqualFold(name, productionDatabase) {
		return errors.New("临时恢复数据库名不安全")
	}
	return nil
}

func (v *Verifier) Verify(ctx context.Context, verification RestoreVerification, backupRun BackupRun) (VerificationResult, error) {
	result := VerificationResult{VerificationID: verification.ID}
	if v == nil || v.restorer == nil || v.runner == nil || backupRun.Status != StatusSucceeded || backupRun.COSSnapshotID == "" {
		result.ErrorMessage = "恢复校验配置或快照无效"
		return result, errors.New(result.ErrorMessage)
	}
	result.TemporaryDatabase = v.newDatabase()
	productionDatabase, err := databaseName(v.configuration.BackupDatabaseURL)
	if err != nil || ValidateTemporaryDatabase(result.TemporaryDatabase, productionDatabase) != nil {
		result.ErrorMessage = "临时恢复数据库名不安全"
		return result, errors.New(result.ErrorMessage)
	}
	directories, err := v.paths.For(verification.ID)
	if err != nil {
		result.ErrorMessage = "创建隔离恢复目录失败"
		return result, errors.New(result.ErrorMessage)
	}

	primaryErr := v.verify(ctx, directories.Restore, result.TemporaryDatabase, backupRun, &result)
	cleanupErr := v.cleanup(ctx, directories, result.TemporaryDatabase)
	if primaryErr == nil && cleanupErr == nil {
		return result, nil
	}
	message := "恢复校验失败"
	if primaryErr == nil {
		message = "恢复校验清理失败"
	} else if cleanupErr != nil {
		message = "恢复校验失败；清理失败"
	}
	result.ErrorMessage = truncate(message, 4096)
	return result, errors.New(result.ErrorMessage)
}

func (v *Verifier) verify(ctx context.Context, restoreRoot, database string, backupRun BackupRun, result *VerificationResult) error {
	if err := v.restorer.RestoreFromCOS(ctx, backupRun.ID, restoreRoot); err != nil {
		return errors.New("从 COS 恢复快照失败")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(restoreRoot, "manifest.json"))
	if err != nil {
		return errors.New("读取恢复清单失败")
	}
	if backupRun.ManifestSHA256 != "" {
		digest := sha256.Sum256(manifestBytes)
		if hex.EncodeToString(digest[:]) != backupRun.ManifestSHA256 {
			return errors.New("恢复清单摘要不一致")
		}
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return errors.New("恢复清单格式无效")
	}
	if err := VerifyManifest(restoreRoot, manifest); err != nil {
		return errors.New("恢复文件完整性校验失败")
	}
	password, err := readSecretFile(v.configuration.VerifyDatabasePasswordFile)
	if err != nil {
		return errors.New("恢复校验数据库凭据不可用")
	}
	defer clearString(&password)
	environment := map[string]string{"PGPASSWORD": password}
	quotedDatabase := pgx.Identifier{database}.Sanitize()
	if _, err := v.runner.Run(ctx, "/usr/bin/psql", []string{
		"--dbname", v.configuration.VerificationDatabaseURL,
		"--set", "ON_ERROR_STOP=1",
		"--command", "CREATE DATABASE " + quotedDatabase,
	}, environment); err != nil {
		return errors.New("创建临时恢复数据库失败")
	}
	targetURL, err := databaseURL(v.configuration.VerificationDatabaseURL, database)
	if err != nil {
		return errors.New("临时恢复数据库地址无效")
	}
	if _, err := v.runner.Run(ctx, "/usr/bin/pg_restore", []string{
		"--exit-on-error", "--no-owner", "--no-privileges",
		"--dbname", targetURL, filepath.Join(restoreRoot, "database", "yunling.dump"),
	}, environment); err != nil {
		return errors.New("数据库恢复失败")
	}
	migration, err := v.runner.Run(ctx, "/usr/bin/psql", []string{
		"--dbname", targetURL, "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1",
		"--command", "SELECT COALESCE(max(version)::text,'0') FROM schema_migrations",
	}, environment)
	if err != nil {
		return errors.New("读取恢复迁移版本失败")
	}
	result.MigrationVersion = strings.TrimSpace(migration.Stdout)
	if result.MigrationVersion == "" {
		return errors.New("恢复迁移版本为空")
	}
	if _, err := v.runner.Run(ctx, "/usr/bin/psql", []string{
		"--dbname", targetURL, "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1",
		"--command", "SELECT (SELECT count(*) FROM users), (SELECT count(*) FROM servers), (SELECT count(*) FROM script_versions), (SELECT count(*) FROM task_runs), (SELECT count(*) FROM audit_logs)",
	}, environment); err != nil {
		return errors.New("恢复关键关系不可读")
	}
	references, err := v.runner.Run(ctx, "/usr/bin/psql", []string{
		"--dbname", targetURL, "--tuples-only", "--no-align", "--set", "ON_ERROR_STOP=1",
		"--command", "SELECT artifact_uri FROM script_versions UNION SELECT object_key FROM run_log_archives UNION SELECT object_key FROM run_artifacts ORDER BY 1",
	}, environment)
	if err != nil {
		return errors.New("读取恢复对象引用失败")
	}
	if err := verifyObjectReferences(manifest, references.Stdout); err != nil {
		return err
	}
	result.CheckedObjects = manifest.ObjectCount
	return nil
}

func (v *Verifier) cleanup(parent context.Context, directories RunDirectories, database string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Minute)
	defer cancel()
	var failures []string
	productionDatabase, _ := databaseName(v.configuration.BackupDatabaseURL)
	if ValidateTemporaryDatabase(database, productionDatabase) == nil {
		password, err := readSecretFile(v.configuration.VerifyDatabasePasswordFile)
		if err != nil {
			failures = append(failures, "数据库清理凭据不可用")
		} else {
			_, dropErr := v.runner.Run(ctx, "/usr/bin/psql", []string{
				"--dbname", v.configuration.VerificationDatabaseURL,
				"--set", "ON_ERROR_STOP=1",
				"--command", "DROP DATABASE IF EXISTS " + pgx.Identifier{database}.Sanitize() + " WITH (FORCE)",
			}, map[string]string{"PGPASSWORD": password})
			clearString(&password)
			if dropErr != nil {
				failures = append(failures, "临时数据库删除失败")
			}
		}
	} else {
		failures = append(failures, "临时数据库名不安全")
	}
	for _, directory := range []string{directories.Restore, directories.Staging} {
		if err := os.RemoveAll(directory); err != nil {
			failures = append(failures, "恢复目录删除失败")
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "；"))
	}
	return nil
}

func verifyObjectReferences(manifest Manifest, output string) error {
	objects := make(map[string]struct{}, len(manifest.Objects))
	for _, object := range manifest.Objects {
		objects[strings.TrimPrefix(object.Path, "objects/")] = struct{}{}
	}
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	references := make([]string, 0, len(lines))
	for _, line := range lines {
		line = normalizeObjectReference(strings.TrimSpace(line))
		if line != "" {
			references = append(references, line)
		}
	}
	sort.Strings(references)
	for _, reference := range references {
		if _, ok := objects[reference]; !ok {
			return fmt.Errorf("恢复对象引用缺失：%s", reference)
		}
	}
	return nil
}

func normalizeObjectReference(value string) string {
	if strings.Contains(value, "://") {
		parts := strings.SplitN(value, "://", 2)
		remainder := parts[1]
		if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
			value = remainder[slash+1:]
		}
	}
	return strings.TrimPrefix(strings.TrimSpace(value), "/")
}

func databaseName(databaseURLValue string) (string, error) {
	parsed, err := url.Parse(databaseURLValue)
	if err != nil {
		return "", err
	}
	name := strings.Trim(parsed.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", errors.New("数据库名无效")
	}
	return name, nil
}

func databaseURL(baseURL, database string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	parsed.Path = "/" + database
	return parsed.String(), nil
}

var _ BackupVerifier = (*Verifier)(nil)
