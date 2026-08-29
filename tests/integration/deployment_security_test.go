package integration_test

import (
	"strings"
	"testing"

	"yunling.local/platform/internal/testpostgres"
)

func TestControlPlaneMasterKeyOwnershipMatchesNonRootService(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	dockerfile := mustReadDeploymentFile(t, root, "deploy", "Dockerfile.services")
	guide := mustReadDeploymentFile(t, root, "deploy", "README.md")
	if !strings.Contains(dockerfile, "USER 10001:10001") {
		t.Fatal("控制面服务镜像必须以非 root UID 10001 运行")
	}
	if !strings.Contains(guide, "chown 10001:10001 deploy/secrets/master.key") ||
		!strings.Contains(guide, "chmod 400 deploy/secrets/master.key") {
		t.Fatal("部署手册必须让非 root 服务 UID 能读取且只有它能读取主密钥")
	}
}
