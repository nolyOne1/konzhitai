package agentupgrade

import (
	"strings"
	"testing"
)

func TestCompareStableVersionsOrdering(t *testing.T) {
	for _, test := range []struct {
		name        string
		left, right string
		want        int
	}{
		{"zero", "0.0.0", "0.0.0", 0},
		{"optional prefix", "v0.2.6", "0.2.6", 0},
		{"both prefixed", "v1.2.3", "v1.2.3", 0},
		{"patch length", "0.2.10", "0.2.6", 1},
		{"patch digits", "0.2.19", "0.2.11", 1},
		{"minor precedence", "0.10.0", "0.9.99", 1},
		{"major precedence", "2.0.0", "1.99.99", 1},
		{"major length", "10.0.0", "9.99.99", 1},
		{"zero versus positive", "0.0.0", "0.0.1", -1},
		{"beyond uint64", "18446744073709551616.0.0", "18446744073709551615.0.0", 1},
		{"large segment length", "0." + strings.Repeat("9", 60) + ".0", "0." + strings.Repeat("9", 59) + ".0", 1},
		{"large segment digits", "0.0." + strings.Repeat("9", 60), "0.0.1" + strings.Repeat("0", 59), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, ok := compareStableVersions(test.left, test.right); !ok || got != test.want {
				t.Fatalf("compare(%q, %q) = (%d, %v), want (%d, true)", test.left, test.right, got, ok, test.want)
			}
			if got, ok := compareStableVersions(test.right, test.left); !ok || got != -test.want {
				t.Fatalf("reverse compare(%q, %q) = (%d, %v), want (%d, true)", test.right, test.left, got, ok, -test.want)
			}
		})
	}
}

func TestCompareStableVersionsRejectsUnknownFormats(t *testing.T) {
	for _, version := range []string{
		"", "v", "1", "1.2", "1.2.3.4", ".1.2", "1..2", "1.2.",
		"01.2.3", "1.02.3", "1.2.03", "00.0.0", "v01.2.3",
		"+1.2.3", "1.+2.3", "1.2.+3", "-1.2.3", "1.-2.3", "1.2.-3",
		"V1.2.3", "vv1.2.3", "release-1.2.3", "latest", "1.a.3",
		"1.2.3-rc.1", "1.2.3+build.1", "v1.2.3-rc.1+build.1",
		" 1.2.3", "1.2.3 ", "\t1.2.3", "1.2.3\n", "1.2.3\r\n", "1. 2.3", "v 1.2.3",
		"1.2.3\x00", "１.2.3", "1.٢.3", "1.2.三", "1\uFF0E2.3", "1.2.3\u00A0",
	} {
		t.Run(version, func(t *testing.T) {
			if got, ok := compareStableVersions(version, "1.2.3"); ok || got != 0 {
				t.Fatalf("invalid left %q: got (%d, %v), want (0, false)", version, got, ok)
			}
			if got, ok := compareStableVersions("1.2.3", version); ok || got != 0 {
				t.Fatalf("invalid right %q: got (%d, %v), want (0, false)", version, got, ok)
			}
		})
	}
}

func TestCompareStableVersionsLengthLimitIncludesPrefix(t *testing.T) {
	for _, test := range []struct {
		name    string
		version string
		length  int
		valid   bool
	}{
		{"64 bytes without prefix", strings.Repeat("9", 60) + ".0.0", 64, true},
		{"64 bytes with prefix", "v" + strings.Repeat("9", 59) + ".0.0", 64, true},
		{"65 bytes without prefix", strings.Repeat("9", 61) + ".0.0", 65, false},
		{"65 bytes with prefix", "v" + strings.Repeat("9", 60) + ".0.0", 65, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if len(test.version) != test.length {
				t.Fatalf("fixture length = %d, want %d", len(test.version), test.length)
			}
			if got, ok := compareStableVersions(test.version, test.version); got != 0 || ok != test.valid {
				t.Fatalf("compare %d-byte version = (%d, %v), want (0, %v)", test.length, got, ok, test.valid)
			}
		})
	}
}
