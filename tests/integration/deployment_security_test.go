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

func TestMinIOUsesFixedSecurityReleaseBuiltFromSource(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	compose := mustReadDeploymentFile(t, root, "deploy", "docker-compose.yml")
	dockerfile := mustReadDeploymentFile(t, root, "deploy", "Dockerfile.minio")
	const release = "RELEASE.2025-10-15T17-29-55Z"
	if !strings.Contains(compose, "dockerfile: deploy/Dockerfile.minio") ||
		!strings.Contains(compose, "MINIO_VERSION: "+release) {
		t.Fatal("MinIO 必须从固定的安全源码版本构建")
	}
	if !strings.Contains(dockerfile, "github.com/minio/minio@${MINIO_VERSION}") ||
		!strings.Contains(dockerfile, "ARG MINIO_VERSION="+release) ||
		!strings.Contains(dockerfile, "GOSUMDB=\"${GOSUMDB}\"") {
		t.Fatal("MinIO 构建文件必须固定官方安全源码版本")
	}
}

func TestServiceBuildUsesReachableVerifiedGoModuleMirror(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	dockerfile := mustReadDeploymentFile(t, root, "deploy", "Dockerfile.services")
	if !strings.Contains(dockerfile, "GOPROXY=https://mirrors.cloud.tencent.com/go,direct") ||
		!strings.Contains(dockerfile, "GOSUMDB=sum.golang.google.cn") {
		t.Fatal("腾讯云服务镜像构建必须使用可达的 Go 模块镜像并保留校验数据库")
	}
}
