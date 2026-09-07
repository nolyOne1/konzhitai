package agentupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

func TestApplySuccessAndTimeoutRollback(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		root, install := writeApplyFixture(t)
		system := &fakeSystem{}
		if err := Apply(root, "upgrade-1", system); err != nil {
			t.Fatal(err)
		}
		if readTestFile(t, filepath.Join(install, "usr/local/bin/yunling-agent")) != "new" {
			t.Fatal("未安装新代理")
		}
	})
	t.Run("超时回滚", func(t *testing.T) {
		root, install := writeApplyFixture(t)
		system := &fakeSystem{waitErr: context.DeadlineExceeded}
		system.restartHook = func(call int) {
			if call != 2 {
				return
			}
			spec, err := loadSpec(root, "upgrade-1")
			if err != nil || spec.Phase != "rolled_back" {
				t.Fatalf("重启旧代理前必须持久化回滚结果：spec=%+v err=%v", spec, err)
			}
			state, err := LoadRuntimeState(root)
			if err != nil || state == nil || state.Stage != agentprotocol.StageFailed || state.ErrorCode != "apply_rolled_back" || state.TargetID != "target-1" {
				t.Fatalf("重启旧代理前必须持久化可上报状态：state=%+v err=%v", state, err)
			}
		}
		if err := Apply(root, "upgrade-1", system); err == nil {
			t.Fatal("必须返回升级失败")
		}
		if readTestFile(t, filepath.Join(install, "usr/local/bin/yunling-agent")) != "old" || system.restarts != 2 {
			t.Fatalf("未恢复旧版本：restarts=%d", system.restarts)
		}
	})
}

func TestExplicitRollbackRestoresBackup(t *testing.T) {
	root, install := writeApplyFixture(t)
	backup := filepath.Join(root, "previous")
	if err := os.MkdirAll(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "yunling-agent"), []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, managed := range managedTargets[1:] {
		if err := os.WriteFile(filepath.Join(backup, managed.name+".missing"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spec := Spec{CommandID: "rollback-1", Action: agentprotocol.UpgradeRollback, Phase: "staged", BackupDir: backup, InstallRoot: install}
	if err := saveSpec(root, spec); err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, "rollback-1", &fakeSystem{}); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, filepath.Join(install, "usr/local/bin/yunling-agent")) != "previous" {
		t.Fatal("未恢复备份代理")
	}
}

func TestApplyResumesAfterFilesWereReplaced(t *testing.T) {
	root, _ := writeApplyFixture(t)
	spec, err := loadSpec(root, "upgrade-1")
	if err != nil {
		t.Fatal(err)
	}
	spec.Phase = "files_replaced"
	if err := saveSpec(root, spec); err != nil {
		t.Fatal(err)
	}
	system := &fakeSystem{}
	if err := Apply(root, "upgrade-1", system); err != nil {
		t.Fatal(err)
	}
	if system.reloads != 1 || system.restarts != 1 {
		t.Fatalf("恢复执行应直接重载并重启：reload=%d restart=%d", system.reloads, system.restarts)
	}
}

func TestApplyRestoresOriginalFilesAfterInterruptedReplacement(t *testing.T) {
	root, install := writeApplyFixture(t)
	spec, err := loadSpec(root, "upgrade-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := backupManaged(spec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "usr/local/bin/yunling-agent"), []byte("partially-new"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec.Phase = "replacing"
	if err := saveSpec(root, spec); err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, "upgrade-1", &fakeSystem{}); err == nil {
		t.Fatal("中断恢复必须报告已执行回滚")
	}
	if got := readTestFile(t, filepath.Join(install, "usr/local/bin/yunling-agent")); got != "old" {
		t.Fatalf("未恢复真实升级前版本：%s", got)
	}
	recovered, err := loadSpec(root, "upgrade-1")
	if err != nil || recovered.Phase != "rolled_back" {
		t.Fatalf("恢复状态错误：%+v %v", recovered, err)
	}
}

func TestApplyRetriesFailedRestoreAfterInterruptedReplacement(t *testing.T) {
	root, install := writeApplyFixture(t)
	spec, err := loadSpec(root, "upgrade-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := backupManaged(spec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "usr/local/bin/yunling-agent"), []byte("partially-new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(spec.BackupDir, "yunling-agent")); err != nil {
		t.Fatal(err)
	}
	spec.Phase = "replacing"
	if err := saveSpec(root, spec); err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, "upgrade-1", &fakeSystem{}); err == nil {
		t.Fatal("备份缺失时恢复必须失败")
	}
	failed, err := loadSpec(root, "upgrade-1")
	if err != nil || failed.Phase != "rollback_failed" {
		t.Fatalf("恢复失败必须保留可重试状态：%+v %v", failed, err)
	}
	if err := os.WriteFile(filepath.Join(spec.BackupDir, "yunling-agent"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, "upgrade-1", &fakeSystem{}); err == nil {
		t.Fatal("恢复成功后仍应报告原升级已失败")
	}
	recovered, err := loadSpec(root, "upgrade-1")
	if err != nil || recovered.Phase != "rolled_back" {
		t.Fatalf("重试恢复状态错误：%+v %v", recovered, err)
	}
	if got := readTestFile(t, filepath.Join(install, "usr/local/bin/yunling-agent")); got != "old" {
		t.Fatalf("重试未恢复原文件：%s", got)
	}
}

func TestApplyRetriesSystemRecoveryAfterFilesWereRestored(t *testing.T) {
	root, _ := writeApplyFixture(t)
	spec, err := loadSpec(root, "upgrade-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := backupManaged(spec); err != nil {
		t.Fatal(err)
	}
	spec.Phase = "replacing"
	if err := saveSpec(root, spec); err != nil {
		t.Fatal(err)
	}
	system := &fakeSystem{reloadErr: errors.New("daemon reload failed")}
	if err := Apply(root, "upgrade-1", system); err == nil {
		t.Fatal("系统服务恢复失败必须返回错误")
	}
	failed, err := loadSpec(root, "upgrade-1")
	if err != nil || failed.Phase != "rollback_failed" {
		t.Fatalf("服务恢复失败必须保留可重试状态：%+v %v", failed, err)
	}
	system.reloadErr = nil
	if err := Apply(root, "upgrade-1", system); err == nil {
		t.Fatal("恢复成功后仍应报告原升级已失败")
	}
	recovered, err := loadSpec(root, "upgrade-1")
	if err != nil || recovered.Phase != "rolled_back" {
		t.Fatalf("系统恢复重试状态错误：%+v %v", recovered, err)
	}
}

type fakeSystem struct {
	waitErr, reloadErr, restartErr error
	reloads, restarts              int
	restartHook                    func(int)
}

func (s *fakeSystem) DaemonReload(context.Context) error { s.reloads++; return s.reloadErr }
func (s *fakeSystem) RestartAgent(context.Context) error {
	s.restarts++
	if s.restartHook != nil {
		s.restartHook(s.restarts)
	}
	return s.restartErr
}
func (s *fakeSystem) WaitForConfirmation(context.Context, string) error { return s.waitErr }
func writeApplyFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	install := t.TempDir()
	current := filepath.Join(install, "usr/local/bin/yunling-agent")
	_ = os.MkdirAll(filepath.Dir(current), 0o700)
	_ = os.WriteFile(current, []byte("old"), 0o755)
	stage := filepath.Join(root, "upgrade-1", "stage")
	_ = os.MkdirAll(stage, 0o700)
	_ = os.WriteFile(filepath.Join(stage, "yunling-agent"), []byte("new"), 0o755)
	archive := []byte("verified archive")
	archivePath := filepath.Join(root, "upgrade-1", "artifact.tar.gz")
	_ = os.WriteFile(archivePath, archive, 0o600)
	digest := sha256.Sum256(archive)
	spec := Spec{CommandID: "upgrade-1", TargetID: "target-1", Action: agentprotocol.UpgradeInstall, TargetVersion: "0.2.0", Phase: "staged", StageDir: stage, ArchivePath: archivePath, SHA256: hex.EncodeToString(digest[:]), ByteSize: int64(len(archive)), BackupDir: filepath.Join(root, "upgrade-1", "backup"), InstallRoot: install, ReconnectTimeout: time.Second}
	if err := saveSpec(root, spec); err != nil {
		t.Fatal(err)
	}
	return root, install
}
func readTestFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

var _ = errors.Is
