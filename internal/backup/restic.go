package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
)

type LocalSnapshotter interface {
	SnapshotLocal(context.Context, string, string) (string, error)
}

type RemoteSnapshotter interface {
	CopyToCOS(context.Context, string, string) (string, error)
}

type ResticRepository struct {
	configuration Config
	runner        CommandExecutor
}

func NewResticRepository(configuration Config, runner CommandExecutor) *ResticRepository {
	return &ResticRepository{configuration: configuration, runner: runner}
}

func (r *ResticRepository) SnapshotLocal(ctx context.Context, root, runID string) (string, error) {
	if r == nil || r.runner == nil || root == "" {
		return "", ErrInvalidRequest
	}
	if _, err := uuid.Parse(runID); err != nil {
		return "", ErrInvalidRequest
	}
	global := []string{
		"--repository-file", r.configuration.LocalRepositoryFile,
		"--password-file", r.configuration.ResticPasswordFile,
	}
	result, err := r.run(ctx, global, "cat", "config")
	if err != nil {
		if result.ExitCode != 10 {
			return "", errors.New("本机备份仓库检查失败")
		}
		if _, err := r.run(ctx, global, "init"); err != nil {
			return "", errors.New("本机备份仓库初始化失败")
		}
	}
	if _, err := r.run(ctx, global, "backup", root, "--tag", "backup-run="+runID); err != nil {
		return "", errors.New("本机加密快照失败")
	}
	snapshots, err := r.run(ctx, global, "snapshots", "--json", "--tag", "backup-run="+runID)
	if err != nil {
		return "", errors.New("查询本机快照失败")
	}
	var matches []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(snapshots.Stdout), &matches); err != nil {
		return "", errors.New("本机快照元数据无效")
	}
	if len(matches) != 1 || strings.TrimSpace(matches[0].ID) == "" {
		return "", fmt.Errorf("本机快照数量异常：%d", len(matches))
	}
	if _, err := r.run(ctx, global, "check", "--read-data-subset=5%"); err != nil {
		return "", errors.New("本机快照完整性检查失败")
	}
	return matches[0].ID, nil
}

func (r *ResticRepository) CopyToCOS(ctx context.Context, localSnapshotID, runID string) (string, error) {
	if r == nil || r.runner == nil || strings.TrimSpace(localSnapshotID) == "" {
		return "", ErrInvalidRequest
	}
	if _, err := uuid.Parse(runID); err != nil {
		return "", ErrInvalidRequest
	}
	if err := r.validateRepositoryFiles(); err != nil {
		return "", err
	}
	accessKey, err := readSecretFile(r.configuration.COSSecretIDFile)
	if err != nil {
		return "", errors.New("COS 访问凭据不可用")
	}
	secretKey, err := readSecretFile(r.configuration.COSSecretKeyFile)
	if err != nil {
		clearString(&accessKey)
		return "", errors.New("COS 访问凭据不可用")
	}
	environment := map[string]string{
		"AWS_ACCESS_KEY_ID":     accessKey,
		"AWS_SECRET_ACCESS_KEY": secretKey,
	}
	defer func() {
		clearString(&accessKey)
		clearString(&secretKey)
		delete(environment, "AWS_ACCESS_KEY_ID")
		delete(environment, "AWS_SECRET_ACCESS_KEY")
	}()
	global := r.cosGlobalArguments()
	result, err := r.runWithEnvironment(ctx, environment, global, "cat", "config")
	if err != nil {
		if result.ExitCode != 10 {
			return "", errors.New("COS 备份仓库检查失败")
		}
		if _, err := r.runWithEnvironment(ctx, environment, global,
			"init",
			"--from-repository-file", r.configuration.LocalRepositoryFile,
			"--from-password-file", r.configuration.ResticPasswordFile,
			"--copy-chunker-params",
		); err != nil {
			return "", errors.New("COS 备份仓库初始化失败")
		}
	}
	if _, err := r.runWithEnvironment(ctx, environment, global,
		"copy",
		"--from-repository-file", r.configuration.LocalRepositoryFile,
		"--from-password-file", r.configuration.ResticPasswordFile,
		localSnapshotID,
	); err != nil {
		return "", errors.New("COS 快照复制失败")
	}
	snapshots, err := r.runWithEnvironment(ctx, environment, global,
		"snapshots", "--json", "--tag", "backup-run="+runID,
	)
	if err != nil {
		return "", errors.New("查询 COS 快照失败")
	}
	var matches []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(snapshots.Stdout), &matches); err != nil {
		return "", errors.New("COS 快照元数据无效")
	}
	if len(matches) != 1 || strings.TrimSpace(matches[0].ID) == "" {
		return "", fmt.Errorf("COS 快照数量异常：%d", len(matches))
	}
	if _, err := r.runWithEnvironment(ctx, environment, global, "check", "--read-data-subset=5%"); err != nil {
		return "", errors.New("COS 快照完整性检查失败")
	}
	return matches[0].ID, nil
}

func (r *ResticRepository) RestoreFromCOS(ctx context.Context, runID, destination string) error {
	if r == nil || r.runner == nil || destination == "" {
		return ErrInvalidRequest
	}
	if _, err := uuid.Parse(runID); err != nil {
		return ErrInvalidRequest
	}
	if err := r.validateRepositoryFiles(); err != nil {
		return err
	}
	accessKey, err := readSecretFile(r.configuration.COSSecretIDFile)
	if err != nil {
		return errors.New("COS 访问凭据不可用")
	}
	secretKey, err := readSecretFile(r.configuration.COSSecretKeyFile)
	if err != nil {
		clearString(&accessKey)
		return errors.New("COS 访问凭据不可用")
	}
	environment := map[string]string{"AWS_ACCESS_KEY_ID": accessKey, "AWS_SECRET_ACCESS_KEY": secretKey}
	defer func() {
		clearString(&accessKey)
		clearString(&secretKey)
		clear(environment)
	}()
	global := r.cosGlobalArguments()
	if _, err := r.runWithEnvironment(ctx, environment, global, "check", "--read-data-subset=5%"); err != nil {
		return errors.New("COS 仓库完整性检查失败")
	}
	snapshots, err := r.runWithEnvironment(ctx, environment, global,
		"snapshots", "--json", "--tag", "backup-run="+runID,
	)
	if err != nil {
		return errors.New("查询 COS 恢复快照失败")
	}
	var matches []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(snapshots.Stdout), &matches); err != nil || len(matches) != 1 || matches[0].ID == "" {
		return errors.New("COS 恢复快照数量异常")
	}
	if _, err := r.runWithEnvironment(ctx, environment, global,
		"restore", matches[0].ID, "--target", destination,
	); err != nil {
		return errors.New("从 COS 恢复快照失败")
	}
	return nil
}

func (r *ResticRepository) ForgetLocal(ctx context.Context, keepWithin string) error {
	if _, err := r.run(ctx, []string{
		"--repository-file", r.configuration.LocalRepositoryFile,
		"--password-file", r.configuration.ResticPasswordFile,
	}, "forget", "--prune", "--keep-within", keepWithin); err != nil {
		return errors.New("本机备份保留清理失败")
	}
	return nil
}

func (r *ResticRepository) ForgetCOS(ctx context.Context, keepWithin string) error {
	if err := r.validateRepositoryFiles(); err != nil {
		return err
	}
	accessKey, err := readSecretFile(r.configuration.COSSecretIDFile)
	if err != nil {
		return errors.New("COS 访问凭据不可用")
	}
	secretKey, err := readSecretFile(r.configuration.COSSecretKeyFile)
	if err != nil {
		clearString(&accessKey)
		return errors.New("COS 访问凭据不可用")
	}
	environment := map[string]string{"AWS_ACCESS_KEY_ID": accessKey, "AWS_SECRET_ACCESS_KEY": secretKey}
	defer func() {
		clearString(&accessKey)
		clearString(&secretKey)
		clear(environment)
	}()
	if _, err := r.runWithEnvironment(ctx, environment, r.cosGlobalArguments(),
		"forget", "--prune", "--keep-within", keepWithin,
	); err != nil {
		return errors.New("COS 备份保留清理失败")
	}
	return nil
}

func (r *ResticRepository) run(ctx context.Context, global []string, command ...string) (CommandResult, error) {
	arguments := append(append([]string(nil), global...), command...)
	return r.runner.Run(ctx, "/usr/bin/restic", arguments, nil)
}

func (r *ResticRepository) runWithEnvironment(ctx context.Context, environment map[string]string, global []string, command ...string) (CommandResult, error) {
	arguments := append(append([]string(nil), global...), command...)
	return r.runner.Run(ctx, "/usr/bin/restic", arguments, environment)
}

func (r *ResticRepository) cosGlobalArguments() []string {
	return []string{
		"-o", "s3.bucket-lookup=dns",
		"-o", "s3.region=" + r.configuration.COSRegion,
		"--repository-file", r.configuration.COSRepositoryFile,
		"--password-file", r.configuration.ResticPasswordFile,
	}
}

func (r *ResticRepository) validateRepositoryFiles() error {
	local, err := os.ReadFile(r.configuration.LocalRepositoryFile)
	if err != nil || strings.TrimSpace(string(local)) == "" {
		return errors.New("本机 Restic repository 配置不可用")
	}
	cos, err := os.ReadFile(r.configuration.COSRepositoryFile)
	if err != nil {
		return errors.New("COS Restic repository 配置不可用")
	}
	expected := "s3:" + strings.TrimRight(r.configuration.COSEndpoint, "/") + "/" +
		r.configuration.COSBucket + "/" + strings.Trim(r.configuration.COSPrefix, "/")
	if strings.TrimSpace(string(cos)) != expected {
		return errors.New("COS Restic repository 配置不匹配")
	}
	return nil
}

var _ LocalSnapshotter = (*ResticRepository)(nil)
var _ RemoteSnapshotter = (*ResticRepository)(nil)
var _ COSRestorer = (*ResticRepository)(nil)
