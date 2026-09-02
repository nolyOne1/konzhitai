package agentrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testArtifact struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	FileName string `json:"file_name"`
	ByteSize int64  `json:"byte_size"`
	SHA256   string `json:"sha256"`
}

type testManifest struct {
	Version   string         `json:"version"`
	Artifacts []testArtifact `json:"artifacts"`
}

func TestCatalogLoadsVerifiedArtifactsAndReturnsCopies(t *testing.T) {
	root, manifest, contents := writeValidCatalog(t)

	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	got := catalog.Manifest()
	if got.Version != "0.1.0" || len(got.Artifacts) != 2 {
		t.Fatalf("清单：%+v", got)
	}
	if got.Artifacts[0].Arch != "amd64" || got.Artifacts[1].Arch != "arm64" {
		t.Fatalf("架构顺序：%+v", got.Artifacts)
	}
	for _, artifact := range got.Artifacts {
		wantURL := "/api/releases/agent/0.1.0/" + artifact.SHA256 + "/" + artifact.FileName
		if artifact.DownloadURL != wantURL {
			t.Fatalf("下载地址：got=%q want=%q", artifact.DownloadURL, wantURL)
		}
	}

	got.Artifacts[0].Arch = "changed"
	if catalog.Manifest().Artifacts[0].Arch != "amd64" {
		t.Fatal("Manifest 必须返回切片副本")
	}

	first := manifest.Artifacts[0]
	file, artifact, err := catalog.Open(manifest.Version, first.SHA256, first.FileName)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(contents[first.FileName]) || artifact.Arch != first.Arch {
		t.Fatalf("打开归档错误：artifact=%+v body=%q", artifact, body)
	}

	if _, _, err := catalog.Open(manifest.Version, strings.Repeat("f", 64), first.FileName); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("未知归档必须返回 ErrArtifactNotFound：%v", err)
	}
}

func TestCatalogRejectsInvalidManifestOrArtifact(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string, manifest *testManifest)
	}{
		{
			name: "缺少归档",
			mutate: func(t *testing.T, root string, manifest *testManifest) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, manifest.Artifacts[0].FileName)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "归档不是普通文件",
			mutate: func(t *testing.T, root string, manifest *testManifest) {
				t.Helper()
				path := filepath.Join(root, manifest.Artifacts[0].FileName)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "大小不符",
			mutate: func(_ *testing.T, _ string, manifest *testManifest) {
				manifest.Artifacts[0].ByteSize++
			},
		},
		{
			name: "摘要不符",
			mutate: func(_ *testing.T, _ string, manifest *testManifest) {
				manifest.Artifacts[0].SHA256 = strings.Repeat("0", 64)
			},
		},
		{
			name: "未知系统",
			mutate: func(_ *testing.T, _ string, manifest *testManifest) {
				manifest.Artifacts[0].OS = "windows"
			},
		},
		{
			name: "未知架构",
			mutate: func(_ *testing.T, _ string, manifest *testManifest) {
				manifest.Artifacts[0].Arch = "ppc64le"
			},
		},
		{
			name: "重复架构",
			mutate: func(_ *testing.T, _ string, manifest *testManifest) {
				manifest.Artifacts[1].Arch = "amd64"
			},
		},
		{
			name: "重复文件标识",
			mutate: func(_ *testing.T, _ string, manifest *testManifest) {
				manifest.Artifacts[1].FileName = manifest.Artifacts[0].FileName
				manifest.Artifacts[1].ByteSize = manifest.Artifacts[0].ByteSize
				manifest.Artifacts[1].SHA256 = manifest.Artifacts[0].SHA256
			},
		},
		{
			name: "缺少 arm64",
			mutate: func(_ *testing.T, _ string, manifest *testManifest) {
				manifest.Artifacts = manifest.Artifacts[:1]
			},
		},
		{
			name: "非法版本",
			mutate: func(_ *testing.T, _ string, manifest *testManifest) {
				manifest.Version = "bad/version"
			},
		},
		{
			name: "文件名包含路径",
			mutate: func(_ *testing.T, _ string, manifest *testManifest) {
				manifest.Artifacts[0].FileName = "nested/archive.tar.gz"
			},
		},
		{
			name: "摘要不是小写十六进制",
			mutate: func(_ *testing.T, _ string, manifest *testManifest) {
				manifest.Artifacts[0].SHA256 = strings.Repeat("A", 64)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, manifest, _ := writeValidCatalog(t)
			test.mutate(t, root, &manifest)
			writeTestManifest(t, root, manifest)
			if _, err := Load(root); err == nil {
				t.Fatal("无效发布目录必须被拒绝")
			}
		})
	}
}

func TestCatalogRejectsSymbolicLinkArtifact(t *testing.T) {
	root, manifest, _ := writeValidCatalog(t)
	first := &manifest.Artifacts[0]
	second := manifest.Artifacts[1]
	linkPath := filepath.Join(root, first.FileName)
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second.FileName, linkPath); err != nil {
		t.Skipf("当前环境不能创建符号链接：%v", err)
	}
	first.ByteSize = second.ByteSize
	first.SHA256 = second.SHA256
	writeTestManifest(t, root, manifest)

	if _, err := Load(root); err == nil {
		t.Fatal("符号链接归档必须被拒绝")
	}
}

func TestCatalogRejectsUnknownAndTrailingJSON(t *testing.T) {
	t.Run("未知字段", func(t *testing.T) {
		root, manifest, _ := writeValidCatalog(t)
		encoded, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded[:len(encoded)-1], []byte(`,"unexpected":true}`)...)
		if err := os.WriteFile(filepath.Join(root, "manifest.json"), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatal("未知 JSON 字段必须被拒绝")
		}
	})

	t.Run("尾随 JSON", func(t *testing.T) {
		root, manifest, _ := writeValidCatalog(t)
		encoded, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, []byte("\n{}")...)
		if err := os.WriteFile(filepath.Join(root, "manifest.json"), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root); err == nil {
			t.Fatal("尾随 JSON 必须被拒绝")
		}
	})
}

func writeValidCatalog(t *testing.T) (string, testManifest, map[string][]byte) {
	t.Helper()
	root := t.TempDir()
	contents := map[string][]byte{
		"yunling-agent-0.1.0-linux-amd64.tar.gz": []byte("amd64 archive"),
		"yunling-agent-0.1.0-linux-arm64.tar.gz": []byte("arm64 archive"),
	}
	manifest := testManifest{Version: "0.1.0"}
	for _, arch := range []string{"amd64", "arm64"} {
		fileName := "yunling-agent-0.1.0-linux-" + arch + ".tar.gz"
		content := contents[fileName]
		digest := sha256.Sum256(content)
		if err := os.WriteFile(filepath.Join(root, fileName), content, 0o600); err != nil {
			t.Fatal(err)
		}
		manifest.Artifacts = append(manifest.Artifacts, testArtifact{
			OS: "linux", Arch: arch, FileName: fileName,
			ByteSize: int64(len(content)), SHA256: hex.EncodeToString(digest[:]),
		})
	}
	writeTestManifest(t, root, manifest)
	return root, manifest, contents
}

func writeTestManifest(t *testing.T, root string, manifest testManifest) {
	t.Helper()
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
