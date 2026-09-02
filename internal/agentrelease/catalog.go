package agentrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var ErrArtifactNotFound = errors.New("代理安装包不存在")

var (
	versionPattern  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)
	fileNamePattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,191}\.tar\.gz$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const maxManifestBytes = 1 << 20

type Artifact struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	FileName    string `json:"file_name"`
	ByteSize    int64  `json:"byte_size"`
	SHA256      string `json:"sha256"`
	DownloadURL string `json:"download_url"`
}

type Manifest struct {
	Version   string     `json:"version"`
	Artifacts []Artifact `json:"artifacts"`
}

type Catalog struct {
	manifest      Manifest
	files         map[string]string
	artifactByKey map[string]Artifact
}

type storedArtifact struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	FileName string `json:"file_name"`
	ByteSize int64  `json:"byte_size"`
	SHA256   string `json:"sha256"`
}

type storedManifest struct {
	Version   string           `json:"version"`
	Artifacts []storedArtifact `json:"artifacts"`
}

func Load(root string) (*Catalog, error) {
	manifestPath := filepath.Join(root, "manifest.json")
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("读取代理发布清单：%w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return nil, errors.New("代理发布清单必须是小于 1 MiB 的普通文件")
	}

	manifestFile, err := os.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("打开代理发布清单：%w", err)
	}
	defer manifestFile.Close()

	decoder := json.NewDecoder(manifestFile)
	decoder.DisallowUnknownFields()
	var stored storedManifest
	if err := decoder.Decode(&stored); err != nil {
		return nil, fmt.Errorf("解析代理发布清单：%w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("代理发布清单包含尾随 JSON")
		}
		return nil, fmt.Errorf("检查代理发布清单结尾：%w", err)
	}
	if !versionPattern.MatchString(stored.Version) {
		return nil, errors.New("代理发布清单版本格式无效")
	}
	if len(stored.Artifacts) != 2 {
		return nil, errors.New("代理发布清单必须同时包含 amd64 和 arm64")
	}

	byArch := make(map[string]Artifact, 2)
	pathsByArch := make(map[string]string, 2)
	seenFileNames := make(map[string]struct{}, 2)
	for _, item := range stored.Artifacts {
		if item.OS != "linux" || (item.Arch != "amd64" && item.Arch != "arm64") {
			return nil, fmt.Errorf("代理发布架构不受支持：%s/%s", item.OS, item.Arch)
		}
		if _, exists := byArch[item.Arch]; exists {
			return nil, fmt.Errorf("代理发布架构重复：%s", item.Arch)
		}
		if !fileNamePattern.MatchString(item.FileName) || filepath.Base(item.FileName) != item.FileName {
			return nil, fmt.Errorf("代理安装包文件名无效：%s", item.FileName)
		}
		if _, exists := seenFileNames[item.FileName]; exists {
			return nil, fmt.Errorf("代理安装包文件名重复：%s", item.FileName)
		}
		seenFileNames[item.FileName] = struct{}{}
		if item.ByteSize <= 0 {
			return nil, fmt.Errorf("代理安装包大小无效：%s", item.FileName)
		}
		if !digestPattern.MatchString(item.SHA256) {
			return nil, fmt.Errorf("代理安装包摘要格式无效：%s", item.FileName)
		}

		path := filepath.Join(root, item.FileName)
		if err := verifyArtifact(path, item); err != nil {
			return nil, err
		}
		artifact := Artifact{
			OS: item.OS, Arch: item.Arch, FileName: item.FileName,
			ByteSize: item.ByteSize, SHA256: item.SHA256,
			DownloadURL: "/api/releases/agent/" + stored.Version + "/" + item.SHA256 + "/" + item.FileName,
		}
		byArch[item.Arch] = artifact
		pathsByArch[item.Arch] = path
	}
	if _, ok := byArch["amd64"]; !ok {
		return nil, errors.New("代理发布清单缺少 amd64")
	}
	if _, ok := byArch["arm64"]; !ok {
		return nil, errors.New("代理发布清单缺少 arm64")
	}

	catalog := &Catalog{
		manifest:      Manifest{Version: stored.Version},
		files:         make(map[string]string, 2),
		artifactByKey: make(map[string]Artifact, 2),
	}
	for _, arch := range []string{"amd64", "arm64"} {
		artifact := byArch[arch]
		key := artifactKey(stored.Version, artifact.SHA256, artifact.FileName)
		catalog.manifest.Artifacts = append(catalog.manifest.Artifacts, artifact)
		catalog.files[key] = pathsByArch[arch]
		catalog.artifactByKey[key] = artifact
	}
	return catalog, nil
}

func (c *Catalog) Manifest() Manifest {
	if c == nil {
		return Manifest{}
	}
	manifest := c.manifest
	manifest.Artifacts = append([]Artifact(nil), c.manifest.Artifacts...)
	return manifest
}

func (c *Catalog) Open(version, digest, fileName string) (*os.File, Artifact, error) {
	if c == nil {
		return nil, Artifact{}, ErrArtifactNotFound
	}
	key := artifactKey(version, digest, fileName)
	path, ok := c.files[key]
	if !ok {
		return nil, Artifact{}, ErrArtifactNotFound
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, Artifact{}, fmt.Errorf("打开代理安装包：%w", err)
	}
	return file, c.artifactByKey[key], nil
}

func (c *Catalog) lookup(version, digest, fileName string) (Artifact, bool) {
	if c == nil {
		return Artifact{}, false
	}
	artifact, ok := c.artifactByKey[artifactKey(version, digest, fileName)]
	return artifact, ok
}

func artifactKey(version, digest, fileName string) string {
	return version + "\x00" + digest + "\x00" + fileName
}

func verifyArtifact(path string, artifact storedArtifact) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("读取代理安装包 %s：%w", artifact.FileName, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("代理安装包不是普通文件：%s", artifact.FileName)
	}
	if info.Size() != artifact.ByteSize {
		return fmt.Errorf("代理安装包大小不符：%s", artifact.FileName)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开代理安装包 %s：%w", artifact.FileName, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("校验代理安装包 %s：%w", artifact.FileName, err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return fmt.Errorf("代理安装包摘要不符：%s", artifact.FileName)
	}
	return nil
}
