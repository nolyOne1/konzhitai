package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"yunling.local/platform/internal/agentprotocol"
)

type DriftScanner struct {
	root string
}

func NewDriftScanner(root string) *DriftScanner {
	return &DriftScanner{root: root}
}

func (s *DriftScanner) Scan(ctx context.Context) ([]agentprotocol.SyncResult, error) {
	scriptsRoot, err := filepath.Abs(filepath.Join(s.root, "scripts"))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(scriptsRoot)
	if os.IsNotExist(err) {
		return []agentprotocol.SyncResult{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取脚本缓存目录：%w", err)
	}
	results := make([]agentprotocol.SyncResult, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !validPathID(entry.Name()) {
			continue
		}
		scriptID := entry.Name()
		manifestBytes, err := os.ReadFile(filepath.Join(scriptsRoot, scriptID, "manifest.json"))
		if err != nil {
			continue
		}
		var current currentManifest
		if json.Unmarshal(manifestBytes, &current) != nil || !validPathID(current.VersionID) {
			continue
		}
		drifted, err := versionDrifted(filepath.Join(scriptsRoot, scriptID, current.VersionID))
		if err != nil {
			return nil, err
		}
		if drifted {
			results = append(results, agentprotocol.SyncResult{
				ScriptID: scriptID, VersionID: current.VersionID, State: agentprotocol.SyncDrifted,
				SHA256: current.SHA256, ErrorCode: "content_mismatch", ErrorMessage: "服务器侧脚本内容与中央版本不一致",
			})
		}
	}
	return results, nil
}

func versionDrifted(versionDir string) (bool, error) {
	trackingBytes, err := os.ReadFile(filepath.Join(versionDir, managedFilesName))
	if err != nil {
		return true, nil
	}
	var tracking managedFiles
	if err := json.Unmarshal(trackingBytes, &tracking); err != nil || len(tracking.Files) == 0 {
		return true, nil
	}
	seen := map[string]bool{}
	for relative, expected := range tracking.Files {
		candidate := filepath.Join(versionDir, filepath.FromSlash(relative))
		if !pathInside(versionDir, candidate) {
			return true, nil
		}
		info, err := os.Lstat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			return true, nil
		}
		contents, err := os.ReadFile(candidate)
		if err != nil {
			return false, err
		}
		digest := sha256.Sum256(contents)
		if hex.EncodeToString(digest[:]) != expected {
			return true, nil
		}
		seen[filepath.Clean(relative)] = true
	}
	err = filepath.WalkDir(versionDir, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(versionDir, candidate)
		if err != nil {
			return err
		}
		if relative == managedFilesName {
			return nil
		}
		if !seen[filepath.Clean(relative)] {
			return errUnexpectedManagedFile
		}
		return nil
	})
	if err == errUnexpectedManagedFile {
		return true, nil
	}
	return false, err
}

var errUnexpectedManagedFile = fmt.Errorf("受管版本中存在额外文件")
