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

type fakeSystem struct {
	waitErr           error
	reloads, restarts int
}

func (s *fakeSystem) DaemonReload(context.Context) error                { s.reloads++; return nil }
func (s *fakeSystem) RestartAgent(context.Context) error                { s.restarts++; return nil }
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
	spec := Spec{CommandID: "upgrade-1", Action: agentprotocol.UpgradeInstall, TargetVersion: "0.2.0", Phase: "staged", StageDir: stage, ArchivePath: archivePath, SHA256: hex.EncodeToString(digest[:]), ByteSize: int64(len(archive)), BackupDir: filepath.Join(root, "upgrade-1", "backup"), InstallRoot: install, ReconnectTimeout: time.Second}
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
