package executor_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"yunling.local/platform/internal/executor"
)

func TestDiscoveryRejectsSymlinkOutsideAllowedRoot(t *testing.T) {
	root := t.TempDir()
	outsideRoot := t.TempDir()
	outside := filepath.Join(outsideRoot, "outside.sh")
	if err := os.WriteFile(outside, []byte("echo outside\n"), 0o640); err != nil {
		t.Fatalf("写入根目录外脚本：%v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.sh")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("当前 Windows 环境不允许创建符号链接：%v", err)
		}
		t.Fatalf("创建越界符号链接：%v", err)
	}

	found, err := executor.NewDiscovery().List(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("扫描允许目录：%v", err)
	}
	if len(found) != 0 {
		t.Fatalf("越界符号链接不得出现在发现结果中：%+v", found)
	}
}

func TestDiscoveryReturnsOnlySupportedRegularTextScripts(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "jobs"), 0o750); err != nil {
		t.Fatalf("创建脚本目录：%v", err)
	}
	validPath := filepath.Join(root, "jobs", "archive.py")
	if err := os.WriteFile(validPath, []byte("print('ok')\n"), 0o640); err != nil {
		t.Fatalf("写入有效脚本：%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "jobs", "notes.txt"), []byte("ignore"), 0o640); err != nil {
		t.Fatalf("写入非脚本文件：%v", err)
	}

	found, err := executor.NewDiscovery().List(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("扫描允许目录：%v", err)
	}
	if len(found) != 1 || found[0].Runtime != "python3" || found[0].RelativePath != filepath.Join("jobs", "archive.py") || !filepath.IsAbs(found[0].AbsolutePath) {
		t.Fatalf("发现结果不正确：%+v", found)
	}
}
