package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version     int            `json:"version"`
	GeneratedAt time.Time      `json:"generatedAt"`
	Database    ManifestFile   `json:"database"`
	Objects     []ManifestFile `json:"objects"`
	Metadata    []ManifestFile `json:"metadata"`
	TotalBytes  int64          `json:"totalBytes"`
	ObjectCount int64          `json:"objectCount"`
}

func BuildManifest(root string) (Manifest, error) {
	return buildManifestAt(root, time.Now().UTC())
}

func buildManifestAt(root string, generatedAt time.Time) (Manifest, error) {
	root = filepath.Clean(root)
	manifest := Manifest{Version: 1, GeneratedAt: generatedAt.UTC(), Objects: []ManifestFile{}, Metadata: []ManifestFile{}}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("清单拒绝符号链接：%s", safeRelativePath(root, path))
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("清单拒绝特殊文件：%s", safeRelativePath(root, path))
		}
		relative := safeRelativePath(root, path)
		if relative == "manifest.json" {
			return nil
		}
		file, err := inspectManifestFile(path, relative)
		if err != nil {
			return err
		}
		manifest.TotalBytes += file.Size
		switch {
		case relative == "database/yunling.dump":
			manifest.Database = file
		case strings.HasPrefix(relative, "objects/"):
			manifest.Objects = append(manifest.Objects, file)
		case strings.HasPrefix(relative, "metadata/"):
			manifest.Metadata = append(manifest.Metadata, file)
		default:
			return fmt.Errorf("清单包含未知路径：%s", relative)
		}
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	if manifest.Database.Path == "" {
		return Manifest{}, errors.New("备份缺少 database/yunling.dump")
	}
	sort.Slice(manifest.Objects, func(i, j int) bool { return manifest.Objects[i].Path < manifest.Objects[j].Path })
	sort.Slice(manifest.Metadata, func(i, j int) bool { return manifest.Metadata[i].Path < manifest.Metadata[j].Path })
	manifest.ObjectCount = int64(len(manifest.Objects))
	return manifest, nil
}

func WriteManifest(root string, manifest Manifest) (string, error) {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("编码备份清单：%w", err)
	}
	path := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return "", fmt.Errorf("写入备份清单：%w", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("重新读取备份清单：%w", err)
	}
	var decoded Manifest
	if err := json.Unmarshal(stored, &decoded); err != nil {
		return "", fmt.Errorf("验证备份清单 JSON：%w", err)
	}
	if err := VerifyManifest(root, decoded); err != nil {
		return "", err
	}
	digest := sha256.Sum256(stored)
	return hex.EncodeToString(digest[:]), nil
}

func VerifyManifest(root string, manifest Manifest) error {
	root = filepath.Clean(root)
	files := make([]ManifestFile, 0, 1+len(manifest.Objects)+len(manifest.Metadata))
	files = append(files, manifest.Database)
	files = append(files, manifest.Objects...)
	files = append(files, manifest.Metadata...)
	seen := make(map[string]struct{}, len(files))
	for _, expected := range files {
		if !validManifestPath(expected.Path) {
			return fmt.Errorf("清单路径无效：%s", expected.Path)
		}
		if _, duplicate := seen[expected.Path]; duplicate {
			return fmt.Errorf("清单路径重复：%s", expected.Path)
		}
		seen[expected.Path] = struct{}{}
		path := filepath.Join(root, filepath.FromSlash(expected.Path))
		if err := ensureInside(root, path); err != nil {
			return fmt.Errorf("清单路径无效：%s", expected.Path)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("清单文件不可读：%s", expected.Path)
		}
		actual, err := inspectManifestFile(path, expected.Path)
		if err != nil {
			return fmt.Errorf("校验清单文件失败：%s", expected.Path)
		}
		if actual.Size != expected.Size || actual.SHA256 != expected.SHA256 {
			return fmt.Errorf("清单文件校验不一致：%s", expected.Path)
		}
	}
	return nil
}

func inspectManifestFile(path, relative string) (ManifestFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return ManifestFile{}, err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return ManifestFile{}, err
	}
	return ManifestFile{Path: relative, Size: size, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func validManifestPath(value string) bool {
	if value == "" || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return cleaned == value && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func safeRelativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "不可解析路径"
	}
	return filepath.ToSlash(relative)
}
