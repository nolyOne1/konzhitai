package release

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileSHA256ReturnsLiteralDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value.txt")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("摘要：got=%s want=%s", got, want)
	}
}

func TestMigrationTreeDigestUsesSortedRelativePathsAndContents(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "nested/000002.up.sql", "select 2;\n")
	writeDigestFile(t, root, "000001.up.sql", "select 1;\n")

	got, err := MigrationTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	const want = "bdc0abf02036c510b44383cf21e1d88630f2ef0613ec5ec54e881aaa4a17774c"
	if got != want {
		t.Fatalf("迁移树摘要：got=%s want=%s", got, want)
	}

	writeDigestFile(t, root, "000001.up.sql", "select 3;\n")
	changed, err := MigrationTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if changed == got {
		t.Fatal("文件内容变化必须改变迁移树摘要")
	}
}

func TestMigrationTreeDigestRejectsEmptyTreesAndSymlinks(t *testing.T) {
	if _, err := MigrationTreeDigest(t.TempDir()); !errors.Is(err, ErrInvalidDigestInput) {
		t.Fatalf("空迁移目录必须失败：%v", err)
	}

	root := t.TempDir()
	target := filepath.Join(root, "target.sql")
	if err := os.WriteFile(target, []byte("select 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "linked.sql")); err != nil {
		t.Skipf("当前平台不能创建符号链接：%v", err)
	}
	if _, err := MigrationTreeDigest(root); !errors.Is(err, ErrInvalidDigestInput) {
		t.Fatalf("包含符号链接的迁移目录必须失败：%v", err)
	}
}

func TestDeploymentContractDigestIsPortableAndOrderIndependent(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "deploy/docker-compose.yml", "services: {}\n")
	writeDigestFile(t, root, "deploy/release-policy.json", "{}\n")
	t.Chdir(root)

	got, err := DeploymentContractDigest([]string{
		"deploy/release-policy.json",
		"deploy/docker-compose.yml",
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "b57fb3c8e8afbc6b5d03cee033eefca6707c3974f9019643e140c19010d6e1ff"
	if got != want {
		t.Fatalf("部署契约摘要：got=%s want=%s", got, want)
	}

	reordered, err := DeploymentContractDigest([]string{
		"deploy/docker-compose.yml",
		"deploy/release-policy.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reordered != got {
		t.Fatalf("参数顺序不应影响摘要：first=%s second=%s", got, reordered)
	}
}

func TestDeploymentContractDigestRejectsTraversalAndDuplicates(t *testing.T) {
	root := t.TempDir()
	writeDigestFile(t, root, "deploy/docker-compose.yml", "services: {}\n")
	t.Chdir(root)

	for _, paths := range [][]string{
		{"../outside"},
		{"deploy/docker-compose.yml", "deploy/./docker-compose.yml"},
	} {
		if _, err := DeploymentContractDigest(paths); !errors.Is(err, ErrInvalidDigestInput) {
			t.Fatalf("危险路径必须失败：paths=%v err=%v", paths, err)
		}
	}
}

func writeDigestFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
