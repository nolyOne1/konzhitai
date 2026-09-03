//go:build releaseintegration

package release

import (
	"strings"
	"testing"
	"time"
)

func TestReleaseIntegrationPolicyAllowsOnlyExactLoopbackImages(t *testing.T) {
	images := Images{
		Services: "127.0.0.1:5000/yunling-services@sha256:" + strings.Repeat("a", 64),
		Web:      "127.0.0.1:5000/yunling-web@sha256:" + strings.Repeat("b", 64),
		Ops:      "127.0.0.1:5000/yunling-ops@sha256:" + strings.Repeat("c", 64),
	}
	policy, err := NewReleaseIntegrationPolicy(42, "nolyone1", images)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, CandidateRunID: 101, RepositoryID: 42,
		SourceSHA: strings.Repeat("d", 40), CreatedAt: time.Now().UTC(), Images: images,
		Compatibility: Compatibility{
			MigrationTreeSHA256: strings.Repeat("e", 64), DeploymentContractSHA256: strings.Repeat("f", 64),
			AgentVersion: "0.1.0", AgentManifestSHA256: strings.Repeat("1", 64),
		},
	}
	if err := ValidateManifest(manifest, policy); err != nil {
		t.Fatalf("精确演练镜像被拒绝：%v", err)
	}
	manifest.Images.Web = "127.0.0.1:5000/yunling-web@sha256:" + strings.Repeat("9", 64)
	if err := ValidateManifest(manifest, policy); err == nil {
		t.Fatal("白名单外的演练镜像必须失败")
	}
	images.Web = "registry.example/yunling-web@sha256:" + strings.Repeat("b", 64)
	if _, err := NewReleaseIntegrationPolicy(42, "nolyone1", images); err == nil {
		t.Fatal("演练策略不得允许非本机仓库")
	}
}
