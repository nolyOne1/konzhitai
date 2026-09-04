package release

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommitSuccessRotatesCurrentAndPrevious(t *testing.T) {
	store := NewStateStore(t.TempDir())
	first := testGHCRRelease(t, 101)
	second := testGHCRRelease(t, 102)
	for _, candidate := range []StoredRelease{first, second} {
		if err := store.SaveValidated(candidate); err != nil {
			t.Fatal(err)
		}
		if err := store.CommitSuccess(candidate); err != nil {
			t.Fatal(err)
		}
	}

	current, err := store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	previous, err := store.LoadPrevious()
	if err != nil {
		t.Fatal(err)
	}
	if current.TargetID != "102" || previous.TargetID != "101" {
		t.Fatalf("状态轮换错误：current=%s previous=%s", current.TargetID, previous.TargetID)
	}
	if current.SuccessfulAt.IsZero() || previous.SuccessfulAt.IsZero() {
		t.Fatal("成功状态必须记录完成时间")
	}
}

func TestLoadCurrentReplaysInterruptedStateTransaction(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)
	first := testGHCRRelease(t, 101)
	second := testGHCRRelease(t, 102)
	if err := store.SaveValidated(first); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitSuccess(first); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveValidated(second); err != nil {
		t.Fatal(err)
	}

	transaction := stateTransaction{Current: second, Previous: &first}
	data, err := json.Marshal(transaction)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, stateTransactionFile), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	current, err := store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	previous, err := store.LoadPrevious()
	if err != nil {
		t.Fatal(err)
	}
	if current.TargetID != "102" || previous.TargetID != "101" {
		t.Fatalf("中断事务未重放：current=%s previous=%s", current.TargetID, previous.TargetID)
	}
	if _, err := os.Stat(filepath.Join(root, stateTransactionFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("事务重放后必须删除日志：%v", err)
	}
}

func TestSaveValidatedRejectsBootstrapAndCannotOverwriteHistory(t *testing.T) {
	store := NewStateStore(t.TempDir())
	if err := store.SaveValidated(StoredRelease{TargetID: "bootstrap", Origin: OriginBootstrap}); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("远程候选不得创建 bootstrap：%v", err)
	}

	first := testGHCRRelease(t, 101)
	if err := store.SaveValidated(first); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.SourceSHA = strings.Repeat("e", 40)
	if err := store.SaveValidated(changed); !errors.Is(err, ErrReleaseExists) {
		t.Fatalf("相同目标历史不得覆盖：%v", err)
	}
}

func TestLoadTargetAllowsOnlySuccessfulHistory(t *testing.T) {
	store := NewStateStore(t.TempDir())
	candidate := testGHCRRelease(t, 101)
	if err := store.SaveValidated(candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadTarget("101"); !errors.Is(err, ErrReleaseNotSuccessful) {
		t.Fatalf("未成功候选不得用于回滚：%v", err)
	}
	if err := store.CommitSuccess(candidate); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadTarget("101")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TargetID != candidate.TargetID || loaded.Images != candidate.Images {
		t.Fatalf("成功历史不匹配：got=%+v want=%+v", loaded, candidate)
	}
}

func TestCreateBootstrapAcceptsOnlyFourExactLocalImagesAndRunsOnce(t *testing.T) {
	store := NewStateStore(t.TempDir())
	images := ServiceImages{
		API:       "yunling-local-bootstrap/api:111111111111",
		Scheduler: "yunling-local-bootstrap/scheduler:222222222222",
		Web:       "yunling-local-bootstrap/web:333333333333",
		Ops:       "yunling-local-bootstrap/ops:444444444444",
	}
	compatibility := validManifest().Compatibility
	createdAt := time.Date(2026, time.September, 3, 9, 0, 0, 0, time.UTC)
	bootstrap, err := store.CreateBootstrap(images, compatibility, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.TargetID != "bootstrap" || bootstrap.Origin != OriginBootstrap {
		t.Fatalf("bootstrap 元数据错误：%+v", bootstrap)
	}
	if _, err := store.CreateBootstrap(images, compatibility, createdAt); !errors.Is(err, ErrReleaseExists) {
		t.Fatalf("bootstrap 不得重复导入：%v", err)
	}

	otherStore := NewStateStore(t.TempDir())
	images.API = imageRef("nolyone1", "yunling-services", 'a')
	if _, err := otherStore.CreateBootstrap(images, compatibility, createdAt); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("bootstrap 不得接受远程镜像：%v", err)
	}
}

func TestAppendAuditWritesStrictJSONLines(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)
	event := AuditEvent{
		Operation: "deploy", TargetID: "101", Status: "succeeded",
		OccurredAt: time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC),
	}
	if err := store.AppendAudit(event); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(root, auditFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != `{"operation":"deploy","target_id":"101","status":"succeeded","occurred_at":"2026-09-03T10:00:00Z"}`+"\n" {
		t.Fatalf("审计行不稳定：%q", contents)
	}
}

func testGHCRRelease(t *testing.T, runID int64) StoredRelease {
	t.Helper()
	manifest := validManifest()
	manifest.CandidateRunID = runID
	release, err := NewStoredRelease(manifest, ManifestPolicy{RepositoryID: 42, Owner: "nolyone1"})
	if err != nil {
		t.Fatal(err)
	}
	release.SuccessfulAt = time.Date(2026, time.September, 3, 8, int(runID%60), 0, 0, time.UTC)
	return release
}
