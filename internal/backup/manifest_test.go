package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildManifestIsStableAndDetectsTampering(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"database/yunling.dump":    "database",
		"objects/z-last.bin":       "last-object",
		"objects/a-first.bin":      "first-object",
		"metadata/deployment.json": `{"migrationVersion":"12"}`,
	}
	for name, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	manifest, err := BuildManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Database.Path != "database/yunling.dump" || len(manifest.Objects) != 2 {
		t.Fatalf("清单内容错误：%+v", manifest)
	}
	if manifest.Objects[0].Path != "objects/a-first.bin" || manifest.Objects[1].Path != "objects/z-last.bin" {
		t.Fatalf("对象路径必须按标准化路径稳定排序：%+v", manifest.Objects)
	}
	if manifest.ObjectCount != 2 || manifest.TotalBytes != int64(len("database")+len("last-object")+len("first-object")+len(`{"migrationVersion":"12"}`)) {
		t.Fatalf("清单汇总错误：%+v", manifest)
	}
	if err := VerifyManifest(root, manifest); err != nil {
		t.Fatal(err)
	}

	tampered := filepath.Join(root, "objects", "a-first.bin")
	if err := os.WriteFile(tampered, []byte("changed-object"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = VerifyManifest(root, manifest)
	if err == nil || !strings.Contains(err.Error(), "objects/a-first.bin") || strings.Contains(err.Error(), "changed-object") {
		t.Fatalf("篡改错误必须只包含文件名：%v", err)
	}
}

func TestWriteManifestProducesRepeatableSHA256(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "database", "yunling.dump")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := WriteManifest(root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := WriteManifest(root, manifest)
	if err != nil || first != second || len(first) != 64 {
		t.Fatalf("清单摘要必须稳定：first=%q second=%q err=%v", first, second, err)
	}
}
