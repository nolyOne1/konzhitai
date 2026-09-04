package release

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const safeDockerignoreFixture = `
.git
.worktrees
.tools
node_modules
apps/web/dist
bin
coverage
work
outputs
**/.env
deploy/secrets
deploy/agent
releases
backups
**/*.pem
**/id_rsa*
**/id_ed25519*
`

func TestValidateBuildSourcesRejectsMovableBaseAndMissingSecretExclusion(t *testing.T) {
	root := newBuildPolicyFixture(t)
	writeBuildPolicyFixture(t, root, "deploy/Dockerfile.services", "FROM alpine:3.24\n")
	writeBuildPolicyFixture(t, root, ".dockerignore", "node_modules\n")
	err := ValidateBuildSources(root)
	if !errors.Is(err, ErrUnsafeBuildSource) {
		t.Fatalf("未固定基础镜像和缺少密钥排除规则必须失败：%v", err)
	}
}

func TestValidateBuildSourcesAcceptsPinnedBasesAndEffectiveNestedExclusions(t *testing.T) {
	root := newBuildPolicyFixture(t)
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	writeBuildPolicyFixture(t, root, "deploy/Dockerfile.services", "FROM --platform=linux/amd64 golang:1.27-alpine@"+digestA+" AS builder\nFROM alpine:3.24@"+digestB+"\n")
	writeBuildPolicyFixture(t, root, ".dockerignore", safeDockerignoreFixture)
	if err := ValidateBuildSources(root); err != nil {
		t.Fatalf("固定来源和有效排除规则被拒绝：%v", err)
	}
}

func TestValidateBuildSourcesRejectsDockerfileWithoutBuildStage(t *testing.T) {
	root := newBuildPolicyFixture(t)
	writeBuildPolicyFixture(t, root, "deploy/Dockerfile.services", "# no build stage\nRUN echo unsafe\n")
	writeBuildPolicyFixture(t, root, ".dockerignore", safeDockerignoreFixture)
	if err := ValidateBuildSources(root); !errors.Is(err, ErrUnsafeBuildSource) {
		t.Fatalf("没有 FROM 的 Dockerfile 必须失败：%v", err)
	}
}

func TestValidateBuildSourcesHonorsLaterDockerignoreNegation(t *testing.T) {
	root := newBuildPolicyFixture(t)
	writeBuildPolicyFixture(t, root, "deploy/Dockerfile.services", "FROM alpine:3.24@sha256:"+strings.Repeat("c", 64)+"\n")
	writeBuildPolicyFixture(t, root, ".dockerignore", `
.git
.worktrees
.tools
node_modules
apps/web/dist
bin
coverage
work
outputs
**/.env
deploy/secrets
deploy/agent
releases
backups
**/*.pem
**/id_rsa*
**/id_ed25519*
!nested/.env
`)
	if err := ValidateBuildSources(root); !errors.Is(err, ErrUnsafeBuildSource) {
		t.Fatalf("后置否定规则重新包含密钥时必须失败：%v", err)
	}
}

func newBuildPolicyFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeBuildPolicyFixture(t *testing.T, root, relative, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
