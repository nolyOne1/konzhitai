package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"yunling.local/platform/internal/agentrelease"
)

func TestParseImportCommand(t *testing.T) {
	configuration, err := parseArgs([]string{"import", "--manifest", "/tmp/release/manifest.json", "--directory", "/tmp/release", "--created-by", "00000000-0000-0000-0000-000000000001", "--notes", "增加自升级器", "--recommend"})
	if err != nil || !configuration.Recommend || configuration.ReleaseNotes != "增加自升级器" || configuration.CreatedBy == "" {
		t.Fatalf("解析导入参数失败：config=%+v err=%v", configuration, err)
	}
}

func TestLoadImportInputOpensBothArchitecturesAndVerifiesManifest(t *testing.T) {
	root := t.TempDir()
	manifest := commandManifest{Version: "0.2.0"}
	for _, arch := range []string{"amd64", "arm64"} {
		body := testArchive("0.2.0")
		fileName := "yunling-agent-0.2.0-linux-" + arch + ".tar.gz"
		if err := os.WriteFile(filepath.Join(root, fileName), body, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		manifest.Artifacts = append(manifest.Artifacts, commandArtifact{OS: "linux", Arch: arch, FileName: fileName, ByteSize: int64(len(body)), SHA256: hex.EncodeToString(digest[:])})
	}
	encoded, _ := json.Marshal(manifest)
	manifestPath := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	importer := &recordingImporter{}
	if _, err := executeImport(context.Background(), importConfiguration{ManifestPath: manifestPath, Directory: root, ReleaseNotes: "说明", Recommend: true, CreatedBy: "00000000-0000-0000-0000-000000000001"}, importer); err != nil {
		t.Fatal(err)
	}
	input := importer.input
	if input.Version != "0.2.0" || len(input.Artifacts) != 2 || !input.Recommend || input.ManifestSHA256 == "" || input.CreatedBy == "" {
		t.Fatalf("导入内容：%+v", input)
	}
	for _, item := range input.Artifacts {
		if item.Body == nil {
			t.Fatalf("%s 未打开", item.Arch)
		}
	}
	if len(importer.input.Artifacts) != 2 {
		t.Fatalf("未把双架构传给服务：%+v", importer.input)
	}
}

type recordingImporter struct{ input agentrelease.ImportInput }

func (i *recordingImporter) Import(_ context.Context, input agentrelease.ImportInput) (agentrelease.Release, error) {
	i.input = input
	return agentrelease.Release{Version: input.Version}, nil
}

func testArchive(version string) []byte {
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for name, value := range map[string]string{"yunling-agent": "bin", "install.sh": "sh", "yunling-agent.service": "unit", "yunling-run@.service": "unit", "50-yunling-agent.rules": "rule", "agent-version": version + "\n", "yunling-agent-upgrade@.service": "unit"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(value))})
		_, _ = io.WriteString(tw, value)
	}
	_ = tw.Close()
	_ = gz.Close()
	return output.Bytes()
}
