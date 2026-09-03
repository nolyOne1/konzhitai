package release

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const ManifestSchemaVersion = 1

var (
	lowerHex40Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	lowerHex64Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ownerPattern      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38})$`)
	versionPattern    = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)
)

type Images struct {
	Services string `json:"services"`
	Web      string `json:"web"`
	Ops      string `json:"ops"`
}

type Compatibility struct {
	MigrationTreeSHA256      string `json:"migration_tree_sha256"`
	DeploymentContractSHA256 string `json:"deployment_contract_sha256"`
	AgentVersion             string `json:"agent_version"`
	AgentManifestSHA256      string `json:"agent_manifest_sha256"`
}

type Manifest struct {
	SchemaVersion  int           `json:"schema_version"`
	CandidateRunID int64         `json:"candidate_run_id"`
	RepositoryID   int64         `json:"repository_id"`
	SourceSHA      string        `json:"source_sha"`
	CreatedAt      time.Time     `json:"created_at"`
	Images         Images        `json:"images"`
	Compatibility  Compatibility `json:"compatibility"`
}

type ManifestPolicy struct {
	RepositoryID int64
	Owner        string
}

func DecodeManifest(reader io.Reader) (Manifest, error) {
	var manifest Manifest
	if err := decodeStrictJSON(reader, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("解码发布清单：%w", err)
	}
	return manifest, nil
}

func ValidateManifest(manifest Manifest, policy ManifestPolicy) error {
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return errors.New("发布清单格式版本无效")
	}
	if manifest.CandidateRunID <= 0 {
		return errors.New("候选运行编号无效")
	}
	if policy.RepositoryID <= 0 || manifest.RepositoryID != policy.RepositoryID {
		return errors.New("候选仓库不受信任")
	}
	if !ownerPattern.MatchString(policy.Owner) {
		return errors.New("候选镜像所有者无效")
	}
	if !lowerHex40Pattern.MatchString(manifest.SourceSHA) {
		return errors.New("源提交摘要无效")
	}
	if manifest.CreatedAt.IsZero() {
		return errors.New("候选创建时间不能为空")
	}
	_, offset := manifest.CreatedAt.Zone()
	if offset != 0 {
		return errors.New("候选创建时间必须使用 UTC")
	}

	allowedImages := map[string]string{
		"services": "ghcr.io/" + policy.Owner + "/yunling-services@sha256:",
		"web":      "ghcr.io/" + policy.Owner + "/yunling-web@sha256:",
		"ops":      "ghcr.io/" + policy.Owner + "/yunling-ops@sha256:",
	}
	for name, value := range map[string]string{
		"services": manifest.Images.Services,
		"web":      manifest.Images.Web,
		"ops":      manifest.Images.Ops,
	} {
		prefix := allowedImages[name]
		if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) || !lowerHex64Pattern.MatchString(value[len(prefix):]) {
			return fmt.Errorf("%s 镜像必须使用允许仓库的不可变摘要", name)
		}
	}

	for name, digest := range map[string]string{
		"迁移树":    manifest.Compatibility.MigrationTreeSHA256,
		"部署契约":   manifest.Compatibility.DeploymentContractSHA256,
		"代理发布清单": manifest.Compatibility.AgentManifestSHA256,
	} {
		if !lowerHex64Pattern.MatchString(digest) {
			return fmt.Errorf("%s摘要无效", name)
		}
	}
	if !versionPattern.MatchString(manifest.Compatibility.AgentVersion) {
		return errors.New("代理版本无效")
	}
	return nil
}
