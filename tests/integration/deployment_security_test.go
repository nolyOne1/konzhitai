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

func TestPasswordChangeTrustsOnlyCaddyOwnedForwardedIP(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	compose := mustReadDeploymentFile(t, root, "deploy", "docker-compose.yml")
	caddy := mustReadDeploymentFile(t, root, "deploy", "Caddyfile")
	apiStart := strings.Index(compose, "\n  api:")
	schedulerStart := strings.Index(compose, "\n  scheduler:")
	if apiStart < 0 || schedulerStart <= apiStart {
		t.Fatal("无法定位 Compose API 服务")
	}
	apiService := compose[apiStart:schedulerStart]
	if strings.Contains(apiService, "\n    ports:") {
		t.Fatal("API 不得直接发布宿主机端口")
	}
	if !strings.Contains(compose, `YUNLING_TRUST_PROXY: "true"`) {
		t.Fatal("受控 Compose 网络中的 API 必须显式启用可信代理来源")
	}
	if !strings.Contains(caddy, "header_up -X-Forwarded-For") ||
		!strings.Contains(caddy, "header_up X-Forwarded-For {remote_host}") {
		t.Fatal("Caddy 必须丢弃客户端转发头并写入直接连接地址")
	}
}

func TestOpsDeploymentUsesMinimumPrivilegesAndControlledEgress(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	compose := mustReadDeploymentFile(t, root, "deploy", "docker-compose.yml")
	start := strings.Index(compose, "\n  ops:")
	if start < 0 {
		t.Fatal("Compose 必须定义独立 ops 服务")
	}
	end := strings.Index(compose[start+1:], "\n  bootstrap:")
	if end < 0 {
		t.Fatal("无法定位 Ops 服务边界")
	}
	opsService := compose[start : start+1+end]
	for _, required := range []string{
		"dockerfile: deploy/Dockerfile.ops",
		"read_only: true",
		"no-new-privileges:true",
		"user: \"10001:10001\"",
		"/run/secrets/yunling-master-key:ro",
		"/tmp:size=32m",
		"- backend",
		"- egress",
	} {
		if !strings.Contains(opsService, required) {
			t.Fatalf("Ops 服务缺少安全配置 %q：\n%s", required, opsService)
		}
	}
	if strings.Contains(opsService, "\n    ports:") || strings.Contains(opsService, "docker.sock") {
		t.Fatal("Ops 不得发布宿主机端口或挂载 Docker Socket")
	}
	if !strings.Contains(compose, "\n  egress:\n") {
		t.Fatal("Compose 必须定义 Ops 专用外网网络")
	}
}
