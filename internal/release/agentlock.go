package release

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"yunling.local/platform/internal/agentrelease"
)

const AgentLockSchemaVersion = 1

var ErrAgentReleaseDrift = errors.New("代理发布内容漂移")

type AgentArtifactLock struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	FileName string `json:"file_name"`
	ByteSize int64  `json:"byte_size"`
	SHA256   string `json:"sha256"`
}

type AgentLock struct {
	SchemaVersion  int                 `json:"schema_version"`
	Version        string              `json:"version"`
	ManifestSHA256 string              `json:"manifest_sha256"`
	Artifacts      []AgentArtifactLock `json:"artifacts"`
}

func LoadAgentLock(path string) (AgentLock, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return AgentLock{}, fmt.Errorf("读取代理锁：%w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return AgentLock{}, errors.New("代理锁必须是小于 1 MiB 的普通文件")
	}
	file, err := os.Open(path)
	if err != nil {
		return AgentLock{}, fmt.Errorf("打开代理锁：%w", err)
	}
	defer file.Close()

	var lock AgentLock
	if err := decodeStrictJSON(file, &lock); err != nil {
		return AgentLock{}, fmt.Errorf("解析代理锁：%w", err)
	}
	if err := validateAgentLock(lock); err != nil {
		return AgentLock{}, err
	}
	return lock, nil
}

func VerifyAgentReleaseDir(lock AgentLock, root string) error {
	if err := validateAgentLock(lock); err != nil {
		return err
	}
	manifestPath := filepath.Join(root, "manifest.json")
	manifestDigest, err := FileSHA256(manifestPath)
	if err != nil {
		return fmt.Errorf("%w：%v", ErrAgentReleaseDrift, err)
	}
	if manifestDigest != lock.ManifestSHA256 {
		return fmt.Errorf("%w：代理清单摘要不符", ErrAgentReleaseDrift)
	}

	catalog, err := agentrelease.Load(root)
	if err != nil {
		return fmt.Errorf("%w：%v", ErrAgentReleaseDrift, err)
	}
	manifest := catalog.Manifest()
	if manifest.Version != lock.Version || len(manifest.Artifacts) != len(lock.Artifacts) {
		return fmt.Errorf("%w：代理清单与锁不一致", ErrAgentReleaseDrift)
	}
	lockedByArch := make(map[string]AgentArtifactLock, len(lock.Artifacts))
	allowedFiles := map[string]struct{}{"manifest.json": {}}
	for _, artifact := range lock.Artifacts {
		lockedByArch[artifact.Arch] = artifact
		allowedFiles[artifact.FileName] = struct{}{}
	}
	for _, artifact := range manifest.Artifacts {
		locked, ok := lockedByArch[artifact.Arch]
		if !ok || locked.OS != artifact.OS || locked.FileName != artifact.FileName ||
			locked.ByteSize != artifact.ByteSize || locked.SHA256 != artifact.SHA256 {
			return fmt.Errorf("%w：%s 架构不一致", ErrAgentReleaseDrift, artifact.Arch)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("%w：读取代理目录：%v", ErrAgentReleaseDrift, err)
	}
	if len(entries) != len(allowedFiles) {
		return fmt.Errorf("%w：代理目录包含未锁定内容", ErrAgentReleaseDrift)
	}
	for _, entry := range entries {
		if _, ok := allowedFiles[entry.Name()]; !ok {
			return fmt.Errorf("%w：未锁定文件 %s", ErrAgentReleaseDrift, entry.Name())
		}
	}
	return nil
}

func validateAgentLock(lock AgentLock) error {
	if lock.SchemaVersion != AgentLockSchemaVersion {
		return fmt.Errorf("%w：代理锁格式版本无效", ErrAgentReleaseDrift)
	}
	if !versionPattern.MatchString(lock.Version) {
		return fmt.Errorf("%w：代理版本无效", ErrAgentReleaseDrift)
	}
	if !lowerHex64Pattern.MatchString(lock.ManifestSHA256) {
		return fmt.Errorf("%w：代理清单摘要无效", ErrAgentReleaseDrift)
	}
	if len(lock.Artifacts) != 2 {
		return fmt.Errorf("%w：代理锁必须包含两个架构", ErrAgentReleaseDrift)
	}
	seenArchitectures := make(map[string]struct{}, 2)
	seenFiles := make(map[string]struct{}, 2)
	for _, artifact := range lock.Artifacts {
		if artifact.OS != "linux" || (artifact.Arch != "amd64" && artifact.Arch != "arm64") {
			return fmt.Errorf("%w：代理架构无效 %s/%s", ErrAgentReleaseDrift, artifact.OS, artifact.Arch)
		}
		if _, exists := seenArchitectures[artifact.Arch]; exists {
			return fmt.Errorf("%w：代理架构重复 %s", ErrAgentReleaseDrift, artifact.Arch)
		}
		seenArchitectures[artifact.Arch] = struct{}{}
		wantFileName := "yunling-agent-" + lock.Version + "-linux-" + artifact.Arch + ".tar.gz"
		if artifact.FileName != wantFileName {
			return fmt.Errorf("%w：代理文件名无效 %s", ErrAgentReleaseDrift, artifact.FileName)
		}
		if _, exists := seenFiles[artifact.FileName]; exists {
			return fmt.Errorf("%w：代理文件名重复 %s", ErrAgentReleaseDrift, artifact.FileName)
		}
		seenFiles[artifact.FileName] = struct{}{}
		if artifact.ByteSize <= 0 || !lowerHex64Pattern.MatchString(artifact.SHA256) {
			return fmt.Errorf("%w：代理归档元数据无效 %s", ErrAgentReleaseDrift, artifact.FileName)
		}
	}
	return nil
}
