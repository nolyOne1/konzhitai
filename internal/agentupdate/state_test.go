package agentupdate

import (
	"os"
	"path/filepath"
	"testing"
)

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
