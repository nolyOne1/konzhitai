//go:build windows

package backup

import "math"

func availableDiskBytes(string) (int64, error) {
	return math.MaxInt64, nil
}
