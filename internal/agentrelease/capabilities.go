package agentrelease

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
)

// Capabilities are declarations from the release manifest, covered by its
// digest and the release source lock. Runtime scheduling still uses the actual
// node heartbeat; a version number never implies support for a feature.
func ValidateCapabilities(capabilities []string) error {
	seen := map[string]bool{}
	for _, capability := range capabilities {
		if seen[capability] || (capability != SelfUpgradeCapability && capability != RunArtifactsCapability) {
			return fmt.Errorf("%w：代理能力声明无效或重复", ErrReleaseInvalid)
		}
		seen[capability] = true
	}
	return nil
}

// Bind imported metadata to the exact manifest used by the CLI or source lock.
// Legacy internal callers may omit the bytes; declared capabilities with an
// externally supplied digest must always bring the manifest that digest covers.
func validateImportManifest(input ImportInput) error {
	if len(input.ManifestJSON) == 0 {
		if input.Capabilities != nil && input.ManifestSHA256 != "" {
			return fmt.Errorf("%w：能力声明缺少原始清单", ErrReleaseInvalid)
		}
		return nil
	}
	if len(input.ManifestJSON) > maxManifestBytes {
		return ErrReleaseInvalid
	}
	digest := sha256.Sum256(input.ManifestJSON)
	if hex.EncodeToString(digest[:]) != input.ManifestSHA256 {
		return fmt.Errorf("%w：代理清单摘要不一致", ErrArtifactMismatch)
	}
	decoder := json.NewDecoder(bytes.NewReader(input.ManifestJSON))
	decoder.DisallowUnknownFields()
	var manifest storedManifest
	if err := decoder.Decode(&manifest); err != nil {
		return ErrReleaseInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrReleaseInvalid
	}
	if manifest.Version != input.Version || !slices.Equal(manifest.Capabilities, input.Capabilities) ||
		(manifest.Capabilities == nil) != (input.Capabilities == nil) || len(manifest.Artifacts) != len(input.Artifacts) {
		return fmt.Errorf("%w：导入内容与代理清单不一致", ErrArtifactMismatch)
	}
	byArch := map[string]storedArtifact{}
	for _, item := range manifest.Artifacts {
		if _, exists := byArch[item.Arch]; exists {
			return ErrReleaseInvalid
		}
		byArch[item.Arch] = item
	}
	for _, item := range input.Artifacts {
		if byArch[item.Arch] != (storedArtifact{OS: item.OS, Arch: item.Arch, FileName: item.FileName, ByteSize: item.ByteSize, SHA256: item.SHA256}) {
			return fmt.Errorf("%w：安装包元数据与代理清单不一致", ErrArtifactMismatch)
		}
	}
	return nil
}
