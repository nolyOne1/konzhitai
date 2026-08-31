package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DeploymentMetadata struct {
	GitRevision      string            `json:"gitRevision"`
	ImageDigests     map[string]string `json:"imageDigests"`
	MigrationVersion string            `json:"migrationVersion"`
	GeneratedAt      time.Time         `json:"generatedAt"`
	ObjectBucket     string            `json:"objectBucket"`
}

type ExportResult struct {
	Root           string
	Manifest       Manifest
	ManifestSHA256 string
	ByteSize       int64
	ObjectCount    int64
}

type DataExporter interface {
	Export(context.Context, BackupRun) (ExportResult, error)
}

type Exporter struct {
	configuration Config
	runner        CommandExecutor
	paths         RunPaths
	metadata      DeploymentMetadata
	now           func() time.Time
}

func NewExporter(configuration Config, runner CommandExecutor, paths RunPaths, metadata DeploymentMetadata, now func() time.Time) *Exporter {
	if now == nil {
		now = time.Now
	}
	return &Exporter{configuration: configuration, runner: runner, paths: paths, metadata: metadata, now: now}
}

func (e *Exporter) Export(ctx context.Context, run BackupRun) (ExportResult, error) {
	if e == nil || e.runner == nil {
		return ExportResult{}, ErrUnavailable
	}
	directories, err := e.paths.For(run.ID)
	if err != nil {
		return ExportResult{}, err
	}
	databaseDirectory := filepath.Join(directories.Staging, "database")
	objectsDirectory := filepath.Join(directories.Staging, "objects")
	metadataDirectory := filepath.Join(directories.Staging, "metadata")
	for _, directory := range []string{databaseDirectory, objectsDirectory, metadataDirectory} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return ExportResult{}, fmt.Errorf("创建备份导出目录：%w", err)
		}
	}
	databasePassword, err := readSecretFile(e.configuration.BackupDatabasePasswordFile)
	if err != nil {
		return ExportResult{}, errors.New("数据库备份凭据不可用")
	}
	dumpPath := filepath.Join(databaseDirectory, "yunling.dump")
	if _, err := e.runner.Run(ctx, "/usr/bin/pg_dump", []string{
		"--format=custom", "--file=" + dumpPath, e.configuration.BackupDatabaseURL,
	}, map[string]string{"PGPASSWORD": databasePassword}); err != nil {
		return ExportResult{}, errors.New("数据库导出失败")
	}
	clearString(&databasePassword)

	accessKey, err := readSecretFile(e.configuration.MinIOAccessKeyFile)
	if err != nil {
		return ExportResult{}, errors.New("对象存储备份凭据不可用")
	}
	secretKey, err := readSecretFile(e.configuration.MinIOSecretKeyFile)
	if err != nil {
		clearString(&accessKey)
		return ExportResult{}, errors.New("对象存储备份凭据不可用")
	}
	endpoint, err := url.Parse(e.configuration.MinIOEndpoint)
	if err != nil || endpoint.Host == "" {
		clearString(&accessKey)
		clearString(&secretKey)
		return ExportResult{}, errors.New("对象存储备份地址无效")
	}
	endpoint.User = url.UserPassword(accessKey, secretKey)
	mcEnvironment := map[string]string{"MC_HOST_local": endpoint.String()}
	_, mirrorErr := e.runner.Run(ctx, "/usr/bin/mc", []string{
		"mirror", "--overwrite", "--remove", "local/" + e.configuration.MinIOBucket, objectsDirectory,
	}, mcEnvironment)
	clearString(&accessKey)
	clearString(&secretKey)
	delete(mcEnvironment, "MC_HOST_local")
	if mirrorErr != nil {
		return ExportResult{}, errors.New("对象存储镜像失败")
	}

	metadata := e.metadata
	metadata.GeneratedAt = e.now().UTC()
	metadata.ObjectBucket = e.configuration.MinIOBucket
	if metadata.ImageDigests == nil {
		metadata.ImageDigests = map[string]string{}
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return ExportResult{}, fmt.Errorf("编码部署元数据：%w", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDirectory, "deployment.json"), encodedMetadata, 0o600); err != nil {
		return ExportResult{}, fmt.Errorf("写入部署元数据：%w", err)
	}
	manifest, err := buildManifestAt(directories.Staging, metadata.GeneratedAt)
	if err != nil {
		return ExportResult{}, err
	}
	manifestSHA256, err := WriteManifest(directories.Staging, manifest)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{
		Root: directories.Staging, Manifest: manifest, ManifestSHA256: manifestSHA256,
		ByteSize: manifest.TotalBytes, ObjectCount: manifest.ObjectCount,
	}, nil
}

func readSecretFile(path string) (string, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSuffix(strings.TrimSuffix(string(value), "\n"), "\r")
	if secret == "" || strings.ContainsRune(secret, '\x00') {
		return "", errors.New("秘密文件为空")
	}
	return secret, nil
}

func clearString(value *string) {
	if value != nil {
		*value = ""
	}
}

var _ DataExporter = (*Exporter)(nil)
