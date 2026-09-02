package backup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRunPathsCreatesPrivateDirectoriesInsideConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	paths := NewRunPaths(root)
	runID := uuid.NewString()
	directories, err := paths.For(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{directories.Staging, directories.LocalRepository, directories.Restore} {
		relative, err := filepath.Rel(root, directory)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("目录逃逸备份根目录：%q relative=%q err=%v", directory, relative, err)
		}
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() {
			t.Fatalf("备份目录未创建：%q err=%v", directory, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("备份目录权限必须是 0700：%q mode=%o", directory, info.Mode().Perm())
		}
	}
}

func TestRunPathsRejectsInvalidUUIDAndSymbolicLinkEscape(t *testing.T) {
	root := t.TempDir()
	paths := NewRunPaths(root)
	if _, err := paths.For("../../production"); err == nil {
		t.Fatal("路径标识必须是 UUID")
	}

	outside := t.TempDir()
	staging := filepath.Join(root, "staging")
	if err := os.Symlink(outside, staging); err != nil {
		t.Skipf("当前系统无法创建测试符号链接：%v", err)
	}
	if _, err := paths.For(uuid.NewString()); err == nil {
		t.Fatal("必须拒绝通过符号链接逃逸暂存根目录")
	}
}
