package executor_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/executor"
)

func TestEnsureKeepsCurrentVersionWhenChecksumFails(t *testing.T) {
	root := t.TempDir()
	oldPath := seedCurrent(t, root, "script-1", "version-1", []byte("echo old\n"))
	cache := executor.NewCache(root, staticDownloader([]byte("corrupted")))
	expected := sha256.Sum256([]byte("expected"))

	_, err := cache.Ensure(context.Background(), agentprotocol.SyncCommand{
		ScriptID: "script-1", VersionID: "version-2", ArtifactURL: "https://control.example/artifact",
		SHA256: hex.EncodeToString(expected[:]),
	})
	if err != executor.ErrChecksumMismatch {
		t.Fatalf("损坏脚本包必须返回校验错误，实际为 %v", err)
	}
	content, err := os.ReadFile(oldPath)
	if err != nil || string(content) != "echo old\n" {
		t.Fatalf("校验失败后旧版本必须保持不变：content=%q err=%v", content, err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "scripts", "script-1", "manifest.json"))
	if err != nil || !bytes.Contains(manifest, []byte("version-1")) {
		t.Fatalf("校验失败后当前版本清单不得切换：manifest=%s err=%v", manifest, err)
	}
}

func TestEnsureRejectsWindowsSpecialPathCharacters(t *testing.T) {
	digest := sha256.Sum256([]byte("archive"))
	_, err := executor.NewCache(t.TempDir(), staticDownloader([]byte("archive"))).Ensure(context.Background(), agentprotocol.SyncCommand{
		ScriptID: "script:alternate-stream", VersionID: "version-1", ArtifactURL: "https://control.example/artifact",
		SHA256: hex.EncodeToString(digest[:]),
	})
	if err != executor.ErrInvalidSyncCommand {
		t.Fatalf("包含 Windows 特殊路径字符的脚本 ID 必须被拒绝：%v", err)
	}
}

func TestEnsureDownloadsVerifiesAndAtomicallySwitchesVersion(t *testing.T) {
	root := t.TempDir()
	archive := scriptArchive(t, "main.sh", []byte("echo new\n"))
	digest := sha256.Sum256(archive)
	cache := executor.NewCache(root, staticDownloader(archive))

	entrypoint, err := cache.Ensure(context.Background(), agentprotocol.SyncCommand{
		ScriptID: "script-1", VersionID: "version-2", ArtifactURL: "https://control.example/artifact",
		SHA256: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatalf("同步有效脚本包：%v", err)
	}
	if !filepath.IsAbs(entrypoint) {
		t.Fatalf("执行入口必须返回绝对路径：%s", entrypoint)
	}
	content, err := os.ReadFile(entrypoint)
	if err != nil || string(content) != "echo new\n" {
		t.Fatalf("同步后的入口文件不正确：content=%q err=%v", content, err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "scripts", "script-1", "manifest.json"))
	if err != nil || !bytes.Contains(manifest, []byte("version-2")) || !bytes.Contains(manifest, []byte(hex.EncodeToString(digest[:]))) {
		t.Fatalf("当前版本清单必须包含版本和校验值：manifest=%s err=%v", manifest, err)
	}
	stagingEntries, err := os.ReadDir(filepath.Join(root, ".staging"))
	if err != nil || len(stagingEntries) != 0 {
		t.Fatalf("成功后临时目录必须清空：entries=%v err=%v", stagingEntries, err)
	}
}

func TestEnsureReplacesDriftedExistingVersion(t *testing.T) {
	root := t.TempDir()
	archive := scriptArchive(t, "main.sh", []byte("echo managed\n"))
	digest := sha256.Sum256(archive)
	command := agentprotocol.SyncCommand{
		ScriptID: "script-1", VersionID: "version-1", ArtifactURL: "https://control.example/artifact",
		SHA256: hex.EncodeToString(digest[:]),
	}
	cache := executor.NewCache(root, staticDownloader(archive))
	entrypoint, err := cache.Ensure(context.Background(), command)
	if err != nil {
		t.Fatalf("首次同步版本：%v", err)
	}
	if err := os.WriteFile(entrypoint, []byte("echo tampered\n"), 0o750); err != nil {
		t.Fatalf("模拟服务器侧漂移：%v", err)
	}
	entrypoint, err = cache.Ensure(context.Background(), command)
	if err != nil {
		t.Fatalf("重新同步漂移版本：%v", err)
	}
	content, err := os.ReadFile(entrypoint)
	if err != nil || string(content) != "echo managed\n" {
		t.Fatalf("同版本重新同步必须恢复中央内容：content=%q err=%v", content, err)
	}
}

type staticDownloader []byte

func (contents staticDownloader) Download(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(contents)), nil
}

func seedCurrent(t *testing.T, root, scriptID, versionID string, content []byte) string {
	t.Helper()
	versionDir := filepath.Join(root, "scripts", scriptID, versionID)
	if err := os.MkdirAll(versionDir, 0o750); err != nil {
		t.Fatalf("创建旧版本目录：%v", err)
	}
	entrypoint := filepath.Join(versionDir, "main.sh")
	if err := os.WriteFile(entrypoint, content, 0o750); err != nil {
		t.Fatalf("写入旧版本：%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", scriptID, "manifest.json"), []byte(`{"version_id":"version-1","sha256":"old"}`), 0o640); err != nil {
		t.Fatalf("写入旧清单：%v", err)
	}
	return entrypoint
}

func scriptArchive(t *testing.T, entrypoint string, content []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	archive := tar.NewWriter(compressed)
	files := []struct {
		name string
		body []byte
	}{{entrypoint, content}, {"manifest.json", []byte(`{"entrypoint":"main.sh","runtime":"bash"}`)}}
	for _, file := range files {
		if err := archive.WriteHeader(&tar.Header{Name: file.name, Mode: 0o750, Size: int64(len(file.body))}); err != nil {
			t.Fatalf("写入测试包头：%v", err)
		}
		if _, err := archive.Write(file.body); err != nil {
			t.Fatalf("写入测试包内容：%v", err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("关闭 tar：%v", err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatalf("关闭 gzip：%v", err)
	}
	return output.Bytes()
}
