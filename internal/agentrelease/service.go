package agentrelease

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"yunling.local/platform/internal/artifact"
)

const maxAgentArtifactBytes = 512 << 20

type Service struct {
	repository Repository
	objects    artifact.Store
	now        func() time.Time
}

func NewService(repository Repository, objects artifact.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, objects: objects, now: now}
}

func (s *Service) Import(ctx context.Context, input ImportInput) (Release, error) {
	return s.importRelease(ctx, input, []string{SelfUpgradeCapability}, true)
}

func (s *Service) importRelease(ctx context.Context, input ImportInput, capabilities []string, validatePackage bool) (Release, error) {
	if s == nil || s.repository == nil || s.objects == nil {
		return Release{}, errors.New("代理版本仓库暂不可用")
	}
	if !versionPattern.MatchString(input.Version) || len(input.ReleaseNotes) > 4000 || len(input.Artifacts) != 2 {
		return Release{}, ErrReleaseInvalid
	}
	existing, err := s.repository.List(ctx)
	if err != nil {
		return Release{}, err
	}
	for _, release := range existing {
		if release.Version == input.Version {
			return Release{}, ErrReleaseExists
		}
	}

	seenArch := map[string]bool{}
	seenFiles := map[string]bool{}
	release := Release{
		Version: input.Version, Status: ReleaseStatusAvailable, ReleaseNotes: input.ReleaseNotes,
		ManifestSHA256: input.ManifestSHA256, Capabilities: append([]string(nil), capabilities...),
		CreatedBy: input.CreatedBy, CreatedAt: s.now().UTC(),
	}
	type pendingObject struct {
		item Artifact
		body []byte
	}
	pending := make([]pendingObject, 0, 2)
	for _, imported := range input.Artifacts {
		if imported.OS != "linux" || (imported.Arch != "amd64" && imported.Arch != "arm64") || seenArch[imported.Arch] {
			return Release{}, ErrReleaseInvalid
		}
		if !fileNamePattern.MatchString(imported.FileName) || filepath.Base(imported.FileName) != imported.FileName || seenFiles[imported.FileName] {
			return Release{}, ErrReleaseInvalid
		}
		if imported.ByteSize <= 0 || imported.ByteSize > maxAgentArtifactBytes || !digestPattern.MatchString(imported.SHA256) || imported.Body == nil {
			return Release{}, ErrReleaseInvalid
		}
		body, err := io.ReadAll(io.LimitReader(imported.Body, imported.ByteSize+1))
		if err != nil {
			return Release{}, fmt.Errorf("读取代理安装包 %s：%w", imported.FileName, err)
		}
		digest := sha256.Sum256(body)
		if int64(len(body)) != imported.ByteSize || hex.EncodeToString(digest[:]) != imported.SHA256 {
			return Release{}, fmt.Errorf("%w：%s", ErrArtifactMismatch, imported.FileName)
		}
		if validatePackage {
			if err := validateAgentArchive(body, input.Version); err != nil {
				return Release{}, err
			}
		}
		item := Artifact{OS: imported.OS, Arch: imported.Arch, FileName: imported.FileName, ByteSize: imported.ByteSize, SHA256: imported.SHA256}
		item.ObjectKey = ObjectKey(input.Version, item)
		seenArch[item.Arch], seenFiles[item.FileName] = true, true
		release.Artifacts = append(release.Artifacts, item)
		pending = append(pending, pendingObject{item: item, body: body})
	}
	if !seenArch["amd64"] || !seenArch["arm64"] {
		return Release{}, ErrReleaseInvalid
	}
	if release.ManifestSHA256 == "" {
		release.ManifestSHA256, err = manifestDigest(release)
		if err != nil {
			return Release{}, err
		}
	}
	if !digestPattern.MatchString(release.ManifestSHA256) {
		return Release{}, ErrReleaseInvalid
	}
	for _, object := range pending {
		if err := s.objects.Put(ctx, object.item.ObjectKey, bytes.NewReader(object.body), object.item.ByteSize, object.item.SHA256); err != nil {
			return Release{}, err
		}
	}
	created, err := s.repository.Create(ctx, release)
	if err != nil {
		return Release{}, err
	}
	if input.Recommend {
		return s.repository.SetRecommended(ctx, created.ID)
	}
	return publicRelease(created), nil
}

func (s *Service) BootstrapFromDirectory(ctx context.Context, root string) error {
	releases, err := s.repository.List(ctx)
	if err != nil {
		return err
	}
	if len(releases) > 0 {
		return nil
	}
	catalog, err := Load(root)
	if err != nil {
		return err
	}
	manifest := catalog.Manifest()
	manifestBytes, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return fmt.Errorf("读取旧代理清单：%w", err)
	}
	manifestHash := sha256.Sum256(manifestBytes)
	input := ImportInput{Version: manifest.Version, ManifestSHA256: hex.EncodeToString(manifestHash[:]), Recommend: true}
	var opened []*os.File
	defer func() {
		for _, file := range opened {
			_ = file.Close()
		}
	}()
	for _, item := range manifest.Artifacts {
		file, _, err := catalog.Open(manifest.Version, item.SHA256, item.FileName)
		if err != nil {
			return err
		}
		opened = append(opened, file)
		input.Artifacts = append(input.Artifacts, ImportArtifact{OS: item.OS, Arch: item.Arch, FileName: item.FileName, ByteSize: item.ByteSize, SHA256: item.SHA256, Body: file})
	}
	_, err = s.importRelease(ctx, input, nil, false)
	return err
}

func (s *Service) List(ctx context.Context) ([]Release, error) { return s.repository.List(ctx) }
func (s *Service) Recommended(ctx context.Context) (Release, error) {
	return s.repository.Recommended(ctx)
}
func (s *Service) SetRecommended(ctx context.Context, id string) (Release, error) {
	return s.repository.SetRecommended(ctx, id)
}
func (s *Service) Withdraw(ctx context.Context, id string) (Release, error) {
	return s.repository.Withdraw(ctx, id)
}

func (s *Service) Open(ctx context.Context, version, digest, fileName string) (io.ReadCloser, Release, Artifact, error) {
	release, item, err := s.repository.FindArtifact(ctx, version, digest, fileName)
	if err != nil {
		return nil, Release{}, Artifact{}, err
	}
	body, err := s.objects.Open(ctx, item.ObjectKey)
	if err != nil {
		return nil, Release{}, Artifact{}, err
	}
	return body, publicRelease(release), publicArtifact(version, item), nil
}

func (s *Service) releaseManifest(ctx context.Context) (Manifest, error) {
	release, err := s.Recommended(ctx)
	if err != nil {
		return Manifest{}, err
	}
	return Manifest{Version: release.Version, Artifacts: append([]Artifact(nil), release.Artifacts...)}, nil
}

func (s *Service) releaseArtifact(ctx context.Context, version, digest, fileName string) (Artifact, error) {
	_, item, err := s.repository.FindArtifact(ctx, version, digest, fileName)
	if err != nil {
		return Artifact{}, err
	}
	return publicArtifact(version, item), nil
}

func (s *Service) openArtifact(ctx context.Context, version, digest, fileName string) (io.ReadCloser, Artifact, error) {
	body, _, item, err := s.Open(ctx, version, digest, fileName)
	return body, item, err
}

func ObjectKey(version string, item Artifact) string {
	return path.Join("agent-releases", version, item.SHA256, item.FileName)
}

func artifactDownloadURL(version string, item Artifact) string {
	return "/api/releases/agent/" + version + "/" + item.SHA256 + "/" + item.FileName
}

func publicArtifact(version string, item Artifact) Artifact {
	item.DownloadURL = artifactDownloadURL(version, item)
	return item
}

func publicRelease(release Release) Release {
	release.Capabilities = append([]string(nil), release.Capabilities...)
	release.Artifacts = append([]Artifact(nil), release.Artifacts...)
	for index := range release.Artifacts {
		release.Artifacts[index] = publicArtifact(release.Version, release.Artifacts[index])
	}
	if release.Capabilities == nil {
		release.Capabilities = []string{}
	}
	if release.Artifacts == nil {
		release.Artifacts = []Artifact{}
	}
	return release
}

func manifestDigest(release Release) (string, error) {
	manifest := storedManifest{Version: release.Version}
	for _, item := range release.Artifacts {
		manifest.Artifacts = append(manifest.Artifacts, storedArtifact{OS: item.OS, Arch: item.Arch, FileName: item.FileName, ByteSize: item.ByteSize, SHA256: item.SHA256})
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateAgentArchive(body []byte, version string) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w：安装包不是有效的 gzip", ErrReleaseInvalid)
	}
	defer gzipReader.Close()
	required := map[string]bool{
		"50-yunling-agent.rules": false, "install.sh": false, "yunling-agent": false,
		"yunling-agent.service": false, "yunling-run@.service": false,
		"agent-version": false, "yunling-agent-upgrade@.service": false,
	}
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w：无法读取安装包", ErrReleaseInvalid)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("%w：安装包包含非普通文件", ErrReleaseInvalid)
		}
		if _, ok := required[header.Name]; !ok || required[header.Name] {
			return fmt.Errorf("%w：安装包文件集合不正确", ErrReleaseInvalid)
		}
		required[header.Name] = true
		if header.Name == "agent-version" {
			value, err := io.ReadAll(io.LimitReader(reader, 256))
			if err != nil || strings.TrimSpace(string(value)) != version {
				return fmt.Errorf("%w：包内版本不一致", ErrReleaseInvalid)
			}
		}
	}
	for _, found := range required {
		if !found {
			return fmt.Errorf("%w：安装包缺少升级文件", ErrReleaseInvalid)
		}
	}
	return nil
}

// ValidateArchive 校验新代理包的固定文件集合及包内版本声明。
func ValidateArchive(body []byte, version string) error {
	return validateAgentArchive(body, version)
}
