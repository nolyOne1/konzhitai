package agentupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfirmReconnectRequiresTargetVersion(t *testing.T) {
	root := t.TempDir()
	spec := Spec{CommandID: "upgrade-1", TargetVersion: "0.2.0", Phase: "restarted"}
	if err := saveSpec(root, spec); err != nil {
		t.Fatal(err)
	}
	if err := ConfirmReconnect(root, "upgrade-1", "0.1.0"); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("错误版本不得确认重连：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "upgrade-1", "connected")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("错误版本不得创建确认标记：%v", err)
	}
	if err := ConfirmReconnect(root, "upgrade-1", "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "upgrade-1", "connected")); err != nil {
		t.Fatalf("缺少重连确认标记：%v", err)
	}
}

func TestStateUsesAtomicReplacement(t *testing.T) {
	root := t.TempDir()
	spec := Spec{CommandID: "upgrade-1", Phase: "staged"}
	if err := saveSpec(root, spec); err != nil {
		t.Fatal(err)
	}
	spec.Phase = "files_replaced"
	if err := saveSpec(root, spec); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSpec(root, spec.CommandID)
	if err != nil || loaded.Phase != "files_replaced" {
		t.Fatalf("状态未原子更新：%+v err=%v", loaded, err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.CommandID, "spec.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("不得残留临时状态文件：%v", err)
	}
}
