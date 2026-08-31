package integration_test

import (
	"strings"
	"testing"

	"yunling.local/platform/internal/testpostgres"
)

func TestControlPlaneMasterKeyOwnershipMatchesNonRootService(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	dockerfile := mustReadDeploymentFile(t, root, "deploy", "Dockerfile.services")
	compose := mustReadDeploymentFile(t, root, "deploy", "docker-compose.yml")
	guide := mustReadDeploymentFile(t, root, "deploy", "README.md")
	if !strings.Contains(dockerfile, "USER 10001:10001") {
		t.Fatal("控制面服务镜像必须以非 root UID 10001 运行")
	}
	if !strings.Contains(compose, "yunling_ops_secrets:/run/secrets:ro") {
		t.Fatal("非 root 控制面只能通过只读专用密钥卷读取主密钥")
	}
	if !strings.Contains(guide, "chown root:root deploy/secrets/yunling-master-key") ||
		!strings.Contains(guide, "chmod 600 deploy/secrets/yunling-master-key") {
		t.Fatal("部署手册必须让宿主机主密钥只对 root 可读")
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
		"YUNLING_MASTER_KEY_FILE: /run/secrets/yunling-master-key",
		"yunling_ops_secrets:/run/secrets:ro",
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

func TestOpsDeploymentUsesDedicatedSecretAndDataVolumes(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	compose := mustReadDeploymentFile(t, root, "deploy", "docker-compose.yml")
	start := strings.Index(compose, "\n  ops:")
	endRelative := strings.Index(compose[start+1:], "\n  bootstrap:")
	if start < 0 || endRelative < 0 {
		t.Fatal("无法定位 Ops 服务")
	}
	opsService := compose[start : start+1+endRelative]
	for _, required := range []string{
		"yunling_ops_secrets:/run/secrets:ro",
		"yunling_ops_data:/var/lib/yunling-ops",
		"./config/local-repository:/run/config/local-repository:ro",
		"./config/cos-repository:/run/config/cos-repository:ro",
		"YUNLING_BACKUP_POSTGRES_PASSWORD_FILE: /run/secrets/backup-postgres-password",
		"YUNLING_VERIFY_POSTGRES_PASSWORD_FILE: /run/secrets/verify-postgres-password",
		"YUNLING_COS_SECRET_ID_FILE: /run/secrets/cos-secret-id",
		"YUNLING_COS_SECRET_KEY_FILE: /run/secrets/cos-secret-key",
		"YUNLING_RESTIC_PASSWORD_FILE: /run/secrets/restic-password",
	} {
		if !strings.Contains(opsService, required) {
			t.Fatalf("Ops 缺少专用文件/卷配置 %q：\n%s", required, opsService)
		}
	}
	for _, forbidden := range []string{
		"YUNLING_COS_SECRET_ID:", "YUNLING_COS_SECRET_KEY:", "YUNLING_RESTIC_PASSWORD:",
		"docker.sock", "postgres_data:/", "minio_data:/", "- /:/",
	} {
		if strings.Contains(opsService, forbidden) {
			t.Fatalf("Ops 包含禁止配置 %q", forbidden)
		}
	}
}

func TestOpsSecretsInitIsOneShotOfflineAndCopiesOnlyToDedicatedVolume(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	compose := mustReadDeploymentFile(t, root, "deploy", "docker-compose.yml")
	start := strings.Index(compose, "\n  ops-secrets-init:")
	endRelative := strings.Index(compose[start+1:], "\n  ops:")
	if start < 0 || endRelative < 0 {
		t.Fatal("Compose 必须定义一次性 ops-secrets-init")
	}
	service := compose[start : start+1+endRelative]
	for _, required := range []string{
		`profiles: ["tools"]`, "network_mode: none", "restart: \"no\"",
		"yunling_ops_secrets:/target", ":/source/", ":ro",
		"install -m 0400 -o 10001 -g 10001",
	} {
		if !strings.Contains(service, required) {
			t.Fatalf("密钥初始化服务缺少 %q：\n%s", required, service)
		}
	}
	if strings.Contains(service, "yunling_ops_data") || strings.Contains(service, "- backend") || strings.Contains(service, "- egress") {
		t.Fatal("密钥初始化服务不得访问数据卷或网络")
	}
}

func TestBackupToolchainAndSecretGeneratorArePinnedAndFailClosed(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	dockerfile := mustReadDeploymentFile(t, root, "deploy", "Dockerfile.ops")
	script := mustReadDeploymentFile(t, root, "deploy", "initialize-ops-secrets.sh")
	environment := mustReadDeploymentFile(t, root, "deploy", ".env.example")
	for _, required := range []string{"postgres:18.6-alpine", "restic/restic:0.19.1", "quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z", "USER 10001:10001"} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Ops 工具链缺少固定版本 %q", required)
		}
	}
	for _, required := range []string{"umask 077", "拒绝覆盖", "/root/yunling-recovery-key.txt", "chmod 600", "YUNLING_COS_ENDPOINT"} {
		if !strings.Contains(script, required) {
			t.Fatalf("密钥初始化脚本缺少安全要求 %q", required)
		}
	}
	if !strings.Contains(environment, "YUNLING_COS_ENDPOINT=https://") {
		t.Fatal("COS endpoint 示例必须使用 HTTPS")
	}
}

func TestBackupSourceAccountsHaveOnlyRequiredPrivileges(t *testing.T) {
	root := testpostgres.RepositoryRoot(t)
	compose := mustReadDeploymentFile(t, root, "deploy", "docker-compose.yml")
	databaseRoles := mustReadDeploymentFile(t, root, "deploy", "database-roles-init.sh")
	minioPolicy := mustReadDeploymentFile(t, root, "deploy", "minio-backup-policy.json")

	for _, required := range []string{
		"ALTER ROLE yunling_backup NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION",
		"GRANT SELECT ON ALL TABLES IN SCHEMA public TO yunling_backup",
		"ALTER ROLE yunling_verifier NOSUPERUSER CREATEDB NOCREATEROLE NOREPLICATION",
	} {
		if !strings.Contains(databaseRoles, required) {
			t.Fatalf("数据库备份账号初始化缺少最小权限约束 %q", required)
		}
	}
	for _, required := range []string{`"s3:ListBucket"`, `"s3:GetObject"`} {
		if !strings.Contains(minioPolicy, required) {
			t.Fatalf("MinIO 备份策略缺少只读权限 %q", required)
		}
	}
	for _, forbidden := range []string{"s3:PutObject", "s3:DeleteObject", "s3:*"} {
		if strings.Contains(minioPolicy, forbidden) {
			t.Fatalf("MinIO 备份策略包含越权动作 %q", forbidden)
		}
	}
	if !strings.Contains(compose, "./database-roles-init.sh:/opt/yunling/database-roles-init.sh:ro") ||
		!strings.Contains(compose, "./minio-backup-policy.json:/opt/yunling/minio-backup-policy.json:ro") {
		t.Fatal("Compose 必须通过只读文件加载数据库账号和 MinIO 策略")
	}
}
