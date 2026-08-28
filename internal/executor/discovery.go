package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const MaxDiscoveredScriptBytes int64 = 1 << 20

type DiscoveredScript struct {
	AbsolutePath string `json:"absolute_path"`
	RelativePath string `json:"relative_path"`
	Runtime      string `json:"runtime"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
}

type Discovery struct{}

func NewDiscovery() *Discovery {
	return &Discovery{}
}

func (d *Discovery) List(ctx context.Context, allowedRoots []string) ([]DiscoveredScript, error) {
	results := make([]DiscoveredScript, 0)
	seen := map[string]bool{}
	for _, configuredRoot := range allowedRoots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		absoluteRoot, err := filepath.Abs(strings.TrimSpace(configuredRoot))
		if err != nil {
			return nil, fmt.Errorf("解析允许脚本目录：%w", err)
		}
		rootInfo, err := os.Lstat(absoluteRoot)
		if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("允许脚本目录无效：%s", configuredRoot)
		}
		resolvedRoot := filepath.Clean(absoluteRoot)
		err = filepath.WalkDir(resolvedRoot, func(candidate string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxDiscoveredScriptBytes {
				return nil
			}
			runtimeName, ok := runtimeForExtension(filepath.Ext(candidate))
			if !ok {
				return nil
			}
			resolvedCandidate := filepath.Clean(candidate)
			if !pathInside(resolvedRoot, resolvedCandidate) || seen[resolvedCandidate] {
				return nil
			}
			contents, err := os.ReadFile(resolvedCandidate)
			if err != nil || !utf8.Valid(contents) || containsZero(contents) {
				return nil
			}
			relative, err := filepath.Rel(resolvedRoot, resolvedCandidate)
			if err != nil {
				return nil
			}
			digest := sha256.Sum256(contents)
			seen[resolvedCandidate] = true
			results = append(results, DiscoveredScript{
				AbsolutePath: resolvedCandidate,
				RelativePath: relative,
				Runtime:      runtimeName,
				SizeBytes:    info.Size(),
				SHA256:       hex.EncodeToString(digest[:]),
			})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("扫描允许脚本目录：%w", err)
		}
	}
	return results, nil
}

func runtimeForExtension(extension string) (string, bool) {
	switch strings.ToLower(extension) {
	case ".sh":
		return "bash", true
	case ".py":
		return "python3", true
	case ".js":
		return "node", true
	case ".ps1":
		return "powershell", true
	default:
		return "", false
	}
}

func containsZero(contents []byte) bool {
	for _, value := range contents {
		if value == 0 {
			return true
		}
	}
	return false
}
