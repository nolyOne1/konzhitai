package executor_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/executor"
)

func TestDriftScannerReportsModifiedCurrentVersion(t *testing.T) {
	root := t.TempDir()
	archive := scriptArchive(t, "main.sh", []byte("echo managed\n"))
	digest := sha256.Sum256(archive)
	cache := executor.NewCache(root, staticDownloader(archive))
	entrypoint, err := cache.Ensure(context.Background(), agentprotocol.SyncCommand{
		ScriptID: "script-1", VersionID: "version-1", ArtifactURL: "https://control.example/artifact",
		SHA256: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatalf("准备受管版本：%v", err)
	}
	if err := os.WriteFile(entrypoint, []byte("echo changed on server\n"), 0o750); err != nil {
		t.Fatalf("模拟服务器侧修改：%v", err)
	}

	results, err := executor.NewDriftScanner(root).Scan(context.Background())
	if err != nil {
		t.Fatalf("扫描版本漂移：%v", err)
	}
	if len(results) != 1 || results[0].ScriptID != "script-1" || results[0].VersionID != "version-1" || results[0].State != agentprotocol.SyncDrifted {
		t.Fatalf("漂移结果不正确：%+v", results)
	}
}
