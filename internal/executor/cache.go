package executor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"yunling.local/platform/internal/agentprotocol"
)

const (
	MaxPackageBytes   int64 = 8 << 20
	MaxExtractedBytes int64 = 8 << 20
	managedFilesName        = ".yunling-files.json"
)

var (
	ErrChecksumMismatch   = errors.New("脚本包校验值不匹配")
	ErrInvalidSyncCommand = errors.New("脚本同步命令无效")
	ErrUnsafeArchive      = errors.New("脚本包包含不安全路径或文件类型")
)

type Downloader interface {
	Download(ctx context.Context, artifactURL string) (io.ReadCloser, error)
}

type Cache struct {
	root       string
	downloader Downloader
	mu         sync.Mutex
}

type currentManifest struct {
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
}

type packageManifest struct {
	Entrypoint string `json:"entrypoint"`
}

type managedFiles struct {
	Files map[string]string `json:"files"`
}

func NewCache(root string, downloader Downloader) *Cache {
	return &Cache{root: root, downloader: downloader}
}

func (c *Cache) Ensure(ctx context.Context, command agentprotocol.SyncCommand) (string, error) {
	if !validPathID(command.ScriptID) || !validPathID(command.VersionID) || strings.TrimSpace(command.ArtifactURL) == "" || !validSHA256(command.SHA256) || c.downloader == nil {
		return "", ErrInvalidSyncCommand
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	root, err := filepath.Abs(c.root)
	if err != nil {
		return "", fmt.Errorf("解析脚本缓存目录：%w", err)
	}
	stagingRoot := filepath.Join(root, ".staging")
	if err := os.MkdirAll(stagingRoot, 0o750); err != nil {
		return "", fmt.Errorf("创建脚本暂存目录：%w", err)
	}
	stagingDir, err := os.MkdirTemp(stagingRoot, command.VersionID+"-")
	if err != nil {
		return "", fmt.Errorf("创建版本暂存目录：%w", err)
	}
	defer os.RemoveAll(stagingDir)

	packagePath := filepath.Join(stagingDir, "package.tar.gz")
	if err := c.downloadAndVerify(ctx, command, packagePath); err != nil {
		return "", err
	}
	contentDir := filepath.Join(stagingDir, "content")
	entrypoint, err := extractPackage(packagePath, contentDir)
	if err != nil {
		return "", err
	}

	scriptDir := filepath.Join(root, "scripts", command.ScriptID)
	if err := os.MkdirAll(scriptDir, 0o750); err != nil {
		return "", fmt.Errorf("创建脚本缓存目录：%w", err)
	}
	versionDir := filepath.Join(scriptDir, command.VersionID)
	if _, err := os.Stat(versionDir); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(contentDir, versionDir); err != nil {
			return "", fmt.Errorf("切换脚本版本目录：%w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("检查脚本版本目录：%w", err)
	} else if err := replaceExistingVersion(contentDir, versionDir, scriptDir, command.VersionID); err != nil {
		return "", err
	}

	manifestBytes, err := json.Marshal(currentManifest{VersionID: command.VersionID, SHA256: strings.ToLower(command.SHA256)})
	if err != nil {
		return "", err
	}
	temporaryManifest, err := os.CreateTemp(scriptDir, ".manifest-*.json")
	if err != nil {
		return "", fmt.Errorf("创建当前版本清单：%w", err)
	}
	temporaryPath := temporaryManifest.Name()
	defer os.Remove(temporaryPath)
	if err := temporaryManifest.Chmod(0o640); err != nil {
		_ = temporaryManifest.Close()
		return "", fmt.Errorf("限制当前版本清单权限：%w", err)
	}
	if _, err := temporaryManifest.Write(manifestBytes); err != nil {
		_ = temporaryManifest.Close()
		return "", fmt.Errorf("写入当前版本清单：%w", err)
	}
	if err := temporaryManifest.Sync(); err != nil {
		_ = temporaryManifest.Close()
		return "", fmt.Errorf("同步当前版本清单：%w", err)
	}
	if err := temporaryManifest.Close(); err != nil {
		return "", fmt.Errorf("关闭当前版本清单：%w", err)
	}
	if err := atomicReplace(temporaryPath, filepath.Join(scriptDir, "manifest.json")); err != nil {
		return "", fmt.Errorf("原子切换当前脚本版本：%w", err)
	}
	absoluteEntrypoint := filepath.Join(versionDir, filepath.FromSlash(entrypoint))
	if !pathInside(versionDir, absoluteEntrypoint) {
		return "", ErrUnsafeArchive
	}
	return absoluteEntrypoint, nil
}

func replaceExistingVersion(contentDir, versionDir, scriptDir, versionID string) error {
	backupDir, err := os.MkdirTemp(scriptDir, ".previous-"+versionID+"-")
	if err != nil {
		return fmt.Errorf("创建旧版本回滚位置：%w", err)
	}
	if err := os.Remove(backupDir); err != nil {
		return fmt.Errorf("准备旧版本回滚位置：%w", err)
	}
	defer os.RemoveAll(backupDir)
	if err := os.Rename(versionDir, backupDir); err != nil {
		return fmt.Errorf("暂存漂移版本：%w", err)
	}
	if err := os.Rename(contentDir, versionDir); err != nil {
		if rollbackErr := os.Rename(backupDir, versionDir); rollbackErr != nil {
			return fmt.Errorf("替换漂移版本失败：%w；恢复旧版本失败：%v", err, rollbackErr)
		}
		return fmt.Errorf("替换漂移版本失败：%w", err)
	}
	return nil
}

func (c *Cache) downloadAndVerify(ctx context.Context, command agentprotocol.SyncCommand, destination string) error {
	body, err := c.downloader.Download(ctx, command.ArtifactURL)
	if err != nil {
		return fmt.Errorf("下载脚本包：%w", err)
	}
	defer body.Close()
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("创建暂存脚本包：%w", err)
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(body, MaxPackageBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("保存暂存脚本包：%w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("关闭暂存脚本包：%w", closeErr)
	}
	if written > MaxPackageBytes {
		return errors.New("脚本包超过 8 MB 上限")
	}
	if !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), command.SHA256) {
		return ErrChecksumMismatch
	}
	return nil
}

func extractPackage(packagePath, destination string) (string, error) {
	file, err := os.Open(packagePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("解压脚本包：%w", err)
	}
	defer compressed.Close()
	if err := os.MkdirAll(destination, 0o750); err != nil {
		return "", err
	}
	reader := tar.NewReader(compressed)
	hashes := map[string]string{}
	var manifest packageManifest
	var total int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("读取脚本包：%w", err)
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return "", ErrUnsafeArchive
		}
		target := filepath.Join(destination, name)
		if !pathInside(destination, target) {
			return "", ErrUnsafeArchive
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return "", err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > MaxExtractedBytes || total+header.Size > MaxExtractedBytes {
				return "", errors.New("脚本包解压内容超过 8 MB 上限")
			}
			total += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return "", err
			}
			mode := os.FileMode(header.Mode) & 0o750
			if mode == 0 {
				mode = 0o640
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return "", err
			}
			hasher := sha256.New()
			_, copyErr := io.Copy(io.MultiWriter(output, hasher), io.LimitReader(reader, header.Size))
			closeErr := output.Close()
			if copyErr != nil || closeErr != nil {
				return "", errors.Join(copyErr, closeErr)
			}
			normalizedName := filepath.ToSlash(name)
			hashes[normalizedName] = hex.EncodeToString(hasher.Sum(nil))
			if normalizedName == "manifest.json" {
				contents, err := os.ReadFile(target)
				if err != nil || json.Unmarshal(contents, &manifest) != nil {
					return "", errors.New("脚本包清单格式不正确")
				}
			}
		default:
			return "", ErrUnsafeArchive
		}
	}
	if !validArchiveEntrypoint(manifest.Entrypoint) {
		return "", errors.New("脚本包缺少有效入口文件")
	}
	entrypointPath := filepath.Join(destination, filepath.FromSlash(manifest.Entrypoint))
	info, err := os.Lstat(entrypointPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("脚本包入口文件不存在")
	}
	tracking, err := json.Marshal(managedFiles{Files: hashes})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(destination, managedFilesName), tracking, 0o640); err != nil {
		return "", fmt.Errorf("写入受管文件清单：%w", err)
	}
	return filepath.ToSlash(filepath.Clean(manifest.Entrypoint)), nil
}

func validPathID(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validArchiveEntrypoint(value string) bool {
	cleaned := filepath.Clean(filepath.FromSlash(value))
	return value != "" && cleaned != "." && cleaned != ".." && !filepath.IsAbs(cleaned) && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

func pathInside(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
