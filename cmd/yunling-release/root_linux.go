//go:build linux

package main

import "os"

func currentUserIsRoot() bool {
	return os.Geteuid() == 0
}
