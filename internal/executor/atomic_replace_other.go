//go:build !windows

package executor

import "os"

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}
