//go:build !linux

package agentupdate

import "os"

// Agent installation and the privileged upgrade helper are Linux-only. Keep
// the state-machine tests usable on other development platforms.
func inheritStateOwner(_ *os.File, _ os.FileInfo) error { return nil }
