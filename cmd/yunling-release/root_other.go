//go:build !linux

package main

func currentUserIsRoot() bool {
	return false
}
