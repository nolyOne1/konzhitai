package release

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var ErrInvalidDigestInput = errors.New("摘要输入无效")

type digestEntry struct {
	path   string
	digest string
}

func FileSHA256(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("读取摘要文件 %s：%w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w：%s 不是普通文件", ErrInvalidDigestInput, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开摘要文件 %s：%w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("计算文件摘要 %s：%w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func MigrationTreeDigest(root string) (string, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("读取迁移目录：%w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w：迁移根路径必须是目录", ErrInvalidDigestInput)
	}

	entries := make([]digestEntry, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w：迁移目录包含非普通文件 %s", ErrInvalidDigestInput, path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		canonical, err := canonicalRelativePath(relative)
		if err != nil {
			return err
		}
		digest, err := FileSHA256(path)
		if err != nil {
			return err
		}
		entries = append(entries, digestEntry{path: canonical, digest: digest})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("扫描迁移目录：%w", err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("%w：迁移目录不能为空", ErrInvalidDigestInput)
	}
	return digestEntries(entries)
}

func DeploymentContractDigest(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", fmt.Errorf("%w：部署契约文件不能为空", ErrInvalidDigestInput)
	}
	entries := make([]digestEntry, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		canonical, err := canonicalRelativePath(path)
		if err != nil {
			return "", err
		}
		if _, exists := seen[canonical]; exists {
			return "", fmt.Errorf("%w：部署契约路径重复 %s", ErrInvalidDigestInput, canonical)
		}
		seen[canonical] = struct{}{}
		digest, err := FileSHA256(filepath.FromSlash(canonical))
		if err != nil {
			return "", err
		}
		entries = append(entries, digestEntry{path: canonical, digest: digest})
	}
	return digestEntries(entries)
}

func canonicalRelativePath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", fmt.Errorf("%w：路径必须是相对路径", ErrInvalidDigestInput)
	}
	canonical := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if canonical == "." || canonical == ".." || strings.HasPrefix(canonical, "../") || strings.ContainsRune(canonical, '\x00') {
		return "", fmt.Errorf("%w：路径越界 %s", ErrInvalidDigestInput, path)
	}
	return canonical, nil
}

func digestEntries(entries []digestEntry) (string, error) {
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].path < entries[right].path
	})
	hash := sha256.New()
	previous := ""
	for _, entry := range entries {
		if entry.path == previous {
			return "", fmt.Errorf("%w：规范化路径重复 %s", ErrInvalidDigestInput, entry.path)
		}
		previous = entry.path
		if _, err := fmt.Fprintf(hash, "%s  %s\n", entry.digest, entry.path); err != nil {
			return "", fmt.Errorf("计算树摘要：%w", err)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
