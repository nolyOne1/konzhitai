//go:build linux

package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"yunling.local/platform/internal/testpostgres"
)

func TestOpsDataInitMakesVolumePrivateAndWritableByServiceUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("所有权行为测试需要 root")
	}
	dataDir, err := os.MkdirTemp("/tmp", "yunling-ops-data-test-")
	if err != nil {
		t.Fatalf("创建临时数据卷失败：%v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	if err := os.Chmod(dataDir, 0o755); err != nil {
		t.Fatalf("设置初始数据卷权限失败：%v", err)
	}

	root := testpostgres.RepositoryRoot(t)
	command := exec.Command("sh", filepath.Join(root, "deploy", "initialize-ops-data.sh"))
	command.Env = append(os.Environ(), "YUNLING_OPS_DATA_DIR="+dataDir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("初始化运维数据卷失败：%v\n%s", err, output)
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("读取数据卷状态失败：%v", err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("无法读取 Linux 文件所有者")
	}
	if owner.Uid != 10001 || owner.Gid != 10001 || info.Mode().Perm() != 0o700 {
		t.Fatalf("运维数据卷必须为 10001:10001 0700，实际 uid=%d gid=%d mode=%o",
			owner.Uid, owner.Gid, info.Mode().Perm())
	}

	probe := exec.Command("sh", "-c", `umask 077; : > "$1/probe"`, "sh", dataDir)
	probe.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 10001, Gid: 10001}}
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("UID 10001 必须能够写入运维数据卷：%v\n%s", err, output)
	}
}
