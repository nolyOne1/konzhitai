package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type LocalSnapshotter interface {
	SnapshotLocal(context.Context, string, string) (string, error)
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

func (r *ResticRepository) run(ctx context.Context, global []string, command ...string) (CommandResult, error) {
	arguments := append(append([]string(nil), global...), command...)
	return r.runner.Run(ctx, "/usr/bin/restic", arguments, nil)
}

var _ LocalSnapshotter = (*ResticRepository)(nil)
