//go:build linux

package agentupdate

import (
	"fmt"
	"os"
	"syscall"
)

// inheritStateOwner keeps helper-written metadata private to the agent that
// owns its directory, even when the upgrade helper itself runs as root.
func inheritStateOwner(file *os.File, directory os.FileInfo) error {
	owner, ok := directory.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("升级状态目录缺少属主信息")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	current, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("升级状态文件缺少属主信息")
	}
	if current.Uid == owner.Uid && current.Gid == owner.Gid {
		return nil
	}
	return file.Chown(int(owner.Uid), int(owner.Gid))
}
