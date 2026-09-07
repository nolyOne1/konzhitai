package agentupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"yunling.local/platform/internal/agentprotocol"
)

func TestStageRejectsUnexpectedArchiveEntry(t *testing.T) {
	archive := validArchive(t)
	archive = makeArchive(t, map[string][]byte{"yunling-agent": []byte("binary"), "unexpected": []byte("bad")})
	_, err := managerWithArchive(t, archive).Stage(context.Background(), commandFor(archive))
	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("必须拒绝额外归档文件：%v", err)
	}
}
func TestStagePersistsIdempotentState(t *testing.T) {
	archive := validArchive(t)
	manager := managerWithArchive(t, archive)
	command := commandFor(archive)
	first, err := manager.Stage(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Stage(context.Background(), command)
	if err != nil || first.CommandID != second.CommandID || manager.downloader.(*memoryDownload).calls != 1 {
		t.Fatalf("重复暂存必须复用状态：first=%+v second=%+v err=%v", first, second, err)
	}
}
func TestStageRejectsArtifactAndVersionMismatch(t *testing.T) {
	archive := validArchive(t)
	t.Run("字节", func(t *testing.T) {
		command := commandFor(archive)
		command.ByteSize++
		_, err := managerWithArchive(t, archive).Stage(context.Background(), command)
		if !errors.Is(err, ErrArtifactMismatch) {
			t.Fatal(err)
		}
	})
	t.Run("摘要", func(t *testing.T) {
		command := commandFor(archive)
		command.SHA256 = strings.Repeat("0", 64)
		_, err := managerWithArchive(t, archive).Stage(context.Background(), command)
		if !errors.Is(err, ErrArtifactMismatch) {
			t.Fatal(err)
		}
	})
	t.Run("版本", func(t *testing.T) {
		command := commandFor(archive)
		manager := managerWithArchive(t, archive)
		manager.binaryVersion = func(context.Context, string) (string, error) { return "0.1.9", nil }
		_, err := manager.Stage(context.Background(), command)
		if !errors.Is(err, ErrVersionMismatch) {
			t.Fatal(err)
		}
	})
}

type memoryDownload struct {
	body  []byte
	calls int
}

func (d *memoryDownload) Download(context.Context, string) (io.ReadCloser, error) {
	d.calls++
	return io.NopCloser(bytes.NewReader(d.body)), nil
}

type noStarter struct{}

func (noStarter) StartUpgrade(context.Context, string) error { return nil }
func managerWithArchive(t *testing.T, body []byte) *Manager {
	t.Helper()
	return NewManager(t.TempDir(), &memoryDownload{body: body}, noStarter{}, func(context.Context, string) (string, error) { return "0.2.0", nil })
}
func commandFor(body []byte) agentprotocol.UpgradeCommand {
	sum := sha256.Sum256(body)
	return agentprotocol.UpgradeCommand{CommandID: "upgrade-1", Action: agentprotocol.UpgradeInstall, SourceVersion: "0.1.0", TargetVersion: "0.2.0", DownloadURL: "https://example.test/agent.tar.gz", ByteSize: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
}
func validArchive(t *testing.T) []byte {
	t.Helper()
	return makeArchive(t, map[string][]byte{"agent-version": []byte("0.2.0\n"), "yunling-agent": []byte("binary"), "install.sh": []byte("sh"), "yunling-agent.service": []byte("unit"), "yunling-run@.service": []byte("unit"), "yunling-agent-upgrade@.service": []byte("unit"), "50-yunling-agent.rules": []byte("rule")})
}
func makeArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
