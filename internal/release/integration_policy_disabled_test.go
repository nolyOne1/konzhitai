//go:build !releaseintegration

package release

import (
	"errors"
	"strings"
	"testing"
)

func TestProductionBuildRejectsReleaseIntegrationOrigin(t *testing.T) {
	release := StoredRelease{
		TargetID: "101", Origin: originReleaseIntegration, SourceSHA: strings.Repeat("a", 40),
		Images: ServiceImages{
			API: "127.0.0.1:5000/yunling-services@sha256:" + strings.Repeat("b", 64), Scheduler: "127.0.0.1:5000/yunling-services@sha256:" + strings.Repeat("b", 64),
			Web: "127.0.0.1:5000/yunling-web@sha256:" + strings.Repeat("c", 64), Ops: "127.0.0.1:5000/yunling-ops@sha256:" + strings.Repeat("d", 64),
		},
		Compatibility: Compatibility{
			MigrationTreeSHA256: strings.Repeat("e", 64), DeploymentContractSHA256: strings.Repeat("f", 64),
			AgentVersion: "0.1.0", AgentManifestSHA256: strings.Repeat("1", 64),
		},
	}
	if _, err := RenderComposeOverride(release); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("生产构建接受了演练来源：%v", err)
	}
	release.validated = true
	if err := NewStateStore(t.TempDir()).SaveValidated(release); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("生产状态仓库接受了演练来源：%v", err)
	}
}
