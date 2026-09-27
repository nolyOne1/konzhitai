//go:build !linux

package testpostgres

import embeddedpostgres "github.com/fergusstrange/embedded-postgres"

// Keep the existing per-test extraction on other platforms. In particular,
// v1.34.0 checks bin/pg_ctl, whereas the Windows archive contains pg_ctl.exe.
func preparedBinaries(embeddedpostgres.Config, string) (string, error) {
	return "", nil
}
