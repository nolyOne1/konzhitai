package integration_test

import (
	"testing"

	"yunling.local/platform/internal/release"
	"yunling.local/platform/internal/testpostgres"
)

func TestCandidateWorkflowMatchesReleaseSecurityPolicy(t *testing.T) {
	if err := release.ValidateWorkflowFiles(testpostgres.RepositoryRoot(t)); err != nil {
		t.Fatalf("真实候选工作流不符合安全策略：%v", err)
	}
}

func TestProductionWorkflowMatchesReleaseSecurityPolicy(t *testing.T) {
	if err := release.ValidateWorkflowFiles(testpostgres.RepositoryRoot(t)); err != nil {
		t.Fatalf("真实生产工作流不符合安全策略：%v", err)
	}
}
