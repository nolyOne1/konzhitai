package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyAgentReleaseDirAcceptsRecordedFilesAndRejectsDrift(t *testing.T) {
	root := t.TempDir()
	lock := writeTestAgentRelease(t, root)
	if err := VerifyAgentReleaseDir(lock, root); err != nil {
		t.Fatalf("合法代理目录被拒绝：%v", err)
	}

	path := filepath.Join(root, lock.Artifacts[0].FileName)
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAgentReleaseDir(lock, root); !errors.Is(err, ErrAgentReleaseDrift) {
		t.Fatalf("代理包漂移必须返回 ErrAgentReleaseDrift：%v", err)
	}
}

func TestVerifyAgentReleaseDirRejectsManifestAndLockMismatch(t *testing.T) {
	root := t.TempDir()
	lock := writeTestAgentRelease(t, root)

	tests := []struct {
		name   string
		mutate func(*AgentLock)
	}{
		{"错误清单摘要", func(value *AgentLock) { value.ManifestSHA256 = strings.Repeat("0", 64) }},
		{"错误文件名", func(value *AgentLock) { value.Artifacts[0].FileName = "other.tar.gz" }},
		{"重复架构", func(value *AgentLock) { value.Artifacts[1].Arch = "amd64" }},
		{"多余归档", func(value *AgentLock) { value.Artifacts = append(value.Artifacts, value.Artifacts[0]) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := lock
			candidate.Artifacts = append([]AgentArtifactLock(nil), lock.Artifacts...)
			test.mutate(&candidate)
			if err := VerifyAgentReleaseDir(candidate, root); !errors.Is(err, ErrAgentReleaseDrift) {
				t.Fatalf("锁与目录不一致必须返回 ErrAgentReleaseDrift：%v", err)
			}
		})
	}
}

func TestLoadAgentLockUsesStrictJSON(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "release-lock.json")
	for _, body := range []string{
		`{"schema_version":1,"schema_version":1}`,
		`{"schema_version":1,"unknown":true}`,
		`{"schema_version":1} {}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadAgentLock(path); err == nil {
			t.Fatalf("不严格的代理锁必须失败：%s", body)
		}
	}
}

func TestLoadAgentLockReturnsValidatedCopy(t *testing.T) {
	root := t.TempDir()
	want := writeTestAgentRelease(t, root)
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "release-lock.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAgentLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != want.Version || got.ManifestSHA256 != want.ManifestSHA256 || len(got.Artifacts) != 2 {
		t.Fatalf("代理锁不匹配：got=%+v want=%+v", got, want)
	}
}

func writeTestAgentRelease(t *testing.T, root string) AgentLock {
	t.Helper()
	version := "0.1.0"
	artifacts := make([]AgentArtifactLock, 0, 2)
	for _, arch := range []string{"amd64", "arm64"} {
		contents := []byte("archive-" + arch)
		fileName := "yunling-agent-" + version + "-linux-" + arch + ".tar.gz"
		if err := os.WriteFile(filepath.Join(root, fileName), contents, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents)
		artifacts = append(artifacts, AgentArtifactLock{
			OS: "linux", Arch: arch, FileName: fileName,
			ByteSize: int64(len(contents)), SHA256: hex.EncodeToString(digest[:]),
		})
	}
	manifest := struct {
		Version   string              `json:"version"`
		Artifacts []AgentArtifactLock `json:"artifacts"`
	}{Version: version, Artifacts: artifacts}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestData)
	return AgentLock{
		SchemaVersion:  1,
		Version:        version,
		ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
		Artifacts:      artifacts,
	}
}
