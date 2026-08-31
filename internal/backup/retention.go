package backup

import (
	"context"
	"fmt"
)

type RetentionRepository interface {
	ForgetLocal(context.Context, string) error
	ForgetCOS(context.Context, string) error
}

type RetentionPolicy interface {
	Apply(context.Context, BackupRun) error
}

type Retention struct{ repository RetentionRepository }

func NewRetention(repository RetentionRepository) *Retention {
	return &Retention{repository: repository}
}

func (r *Retention) Apply(ctx context.Context, completedRun BackupRun) error {
	if r == nil || r.repository == nil {
		return ErrUnavailable
	}
	if completedRun.Status != StatusSucceeded || completedRun.LocalSnapshotID == "" || completedRun.COSSnapshotID == "" {
		return nil
	}
	if err := r.repository.ForgetLocal(ctx, "7d"); err != nil {
		return fmt.Errorf("本机保留策略失败：%w", err)
	}
	if err := r.repository.ForgetCOS(ctx, "30d"); err != nil {
		return fmt.Errorf("COS 保留策略失败：%w", err)
	}
	return nil
}

var _ RetentionPolicy = (*Retention)(nil)
