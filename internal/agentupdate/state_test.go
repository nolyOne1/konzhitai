package agentupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"yunling.local/platform/internal/agentprotocol"
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

func TestStateWritesDoNotReuseLegacyTemporaryFiles(t *testing.T) {
	for _, kind := range []string{"spec", "runtime"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			spec := Spec{CommandID: "upgrade-1", TargetVersion: "0.2.1", Phase: "staged"}
			if err := saveSpec(root, spec); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "upgrade-1", "spec.json")
			write := func() error {
				spec.Phase = "files_replaced"
				return saveSpec(root, spec)
			}
			if kind == "runtime" {
				path = filepath.Join(root, "runtime.json")
				write = func() error {
					return SaveRuntimeState(root, agentprotocol.UpgradeRuntimeState{
						CommandID: "upgrade-1", Stage: agentprotocol.StageFailed,
						ErrorCode: "apply_rolled_back",
					})
				}
			}
			legacy := path + ".tmp"
			if err := os.WriteFile(legacy, []byte("do-not-overwrite"), 0o600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := write(); err != nil {
					t.Fatal(err)
				}
			}
			if got := readTestFile(t, legacy); got != "do-not-overwrite" {
				t.Fatalf("不得重用遗留临时路径：%q", got)
			}
			matches, err := filepath.Glob(path + ".tmp-*")
			if err != nil || len(matches) != 0 {
				t.Fatalf("不得残留新临时文件：%v err=%v", matches, err)
			}
			if kind == "spec" {
				loaded, err := loadSpec(root, spec.CommandID)
				if err != nil || loaded.Phase != "files_replaced" {
					t.Fatalf("未保存新阶段：%+v err=%v", loaded, err)
				}
			} else {
				loaded, err := LoadRuntimeState(root)
				if err != nil || loaded == nil || loaded.ErrorCode != "apply_rolled_back" {
					t.Fatalf("未保存回滚结果：%+v err=%v", loaded, err)
				}
			}
		})
	}
}

func TestStateWriteFailureCleansTemporaryFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeStateFile(path, []byte("new")); err == nil {
		t.Fatal("不得覆盖现有目录")
	}
	if got := readTestFile(t, marker); got != "keep" {
		t.Fatalf("失败写入改变了原目标：%q", got)
	}
	matches, err := filepath.Glob(path + ".tmp-*")
	if err != nil || len(matches) != 0 {
		t.Fatalf("失败后未清理临时文件：%v err=%v", matches, err)
	}
}
