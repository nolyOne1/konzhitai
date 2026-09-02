package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type retentionFakeRepository struct {
	calls []string
	fail  string
}

func (r *retentionFakeRepository) ForgetLocal(context.Context, string) error {
	r.calls = append(r.calls, "local:7d")
	if r.fail == "local" {
		return errors.New("local prune failed")
	}
	return nil
}

func (r *retentionFakeRepository) ForgetCOS(context.Context, string) error {
	r.calls = append(r.calls, "cos:30d")
	if r.fail == "cos" {
		return errors.New("cos prune failed")
	}
	return nil
}

func TestRetentionRunsOnlyForCompleteSuccessfulBackup(t *testing.T) {
	for _, run := range []BackupRun{
		{Status: StatusUploading, LocalSnapshotID: "local", COSSnapshotID: "cos"},
		{Status: StatusSucceeded, LocalSnapshotID: "local"},
		{Status: StatusSucceeded, COSSnapshotID: "cos"},
	} {
		repository := &retentionFakeRepository{}
		if err := NewRetention(repository).Apply(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		if len(repository.calls) != 0 {
			t.Fatalf("不完整的新备份不得触发清理：run=%+v calls=%v", run, repository.calls)
		}
	}

	repository := &retentionFakeRepository{}
	if err := NewRetention(repository).Apply(context.Background(), BackupRun{
		Status: StatusSucceeded, LocalSnapshotID: "local", COSSnapshotID: "cos",
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(repository.calls, ",") != "local:7d,cos:30d" {
		t.Fatalf("保留策略调用错误：%v", repository.calls)
	}
}

func TestRetentionReturnsPruneFailureWithoutChangingBackup(t *testing.T) {
	repository := &retentionFakeRepository{fail: "cos"}
	err := NewRetention(repository).Apply(context.Background(), BackupRun{
		Status: StatusSucceeded, LocalSnapshotID: "local", COSSnapshotID: "cos",
	})
	if err == nil || !strings.Contains(err.Error(), "COS") {
		t.Fatalf("COS 清理失败应返回有界阶段错误：%v", err)
	}
}
