//go:build !linux

package release

func platformFreeBytes(string) (uint64, error) {
	return 0, ErrUnsupportedPlatform
}

func platformAvailableMemory() (uint64, error) {
	return 0, ErrUnsupportedPlatform
}

func platformTryLock(string) (func() error, error) {
	return nil, ErrUnsupportedPlatform
}
