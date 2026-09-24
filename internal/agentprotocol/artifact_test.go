package agentprotocol

import (
	"strings"
	"testing"
)

func TestArtifactPolicyRejectsPathsAndUnboundedSizes(t *testing.T) {
	for _, policy := range []*ArtifactPolicy{
		{AllowedGlobs: []string{"../*.csv"}, MaxFileBytes: 1, MaxTotalBytes: 2},
		{AllowedGlobs: []string{"*.csv"}},
		{AllowedGlobs: []string{"*.csv"}, MaxFileBytes: 3, MaxTotalBytes: 2},
		{AllowedGlobs: []string{"["}, MaxFileBytes: 1, MaxTotalBytes: 2},
	} {
		if policy.Validate() == nil {
			t.Fatalf("invalid policy accepted: %+v", policy)
		}
	}
	policy := &ArtifactPolicy{AllowedGlobs: []string{"*"}, MaxFileBytes: 10, MaxTotalBytes: 20}
	if policy.Validate() != nil || !policy.Allows("report.csv") {
		t.Fatal("valid policy rejected")
	}
	for _, name := range []string{"../private.csv", ".env", "systemd-run-spec.json", "stdout.log", "folder/report.csv", "tab\t.csv", "bad\xff.csv", strings.Repeat("中", 86)} {
		if policy.Allows(name) {
			t.Fatalf("private artifact allowed: %s", name)
		}
	}
}
